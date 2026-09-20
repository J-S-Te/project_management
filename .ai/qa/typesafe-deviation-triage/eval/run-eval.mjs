import fs from "node:fs/promises";

const fixturePath = process.argv[2] || new URL("./fixtures.jsonl", import.meta.url).pathname;
const runs = Math.max(1, Math.min(5, Number(process.env.TYPESAFE_EVAL_RUNS || 3)));
const apiKey = String(process.env.TYPESAFE_API_KEY || "").trim();
const endpoint = String(process.env.TYPESAFE_API_URL || "https://api.typesafe.ai/v1/systemone").trim();
if (!apiKey) throw new Error("TYPESAFE_API_KEY is required");

const lines = (await fs.readFile(fixturePath, "utf8")).split(/\r?\n/).filter(Boolean);
const fixtures = lines.map((line) => JSON.parse(line));
const questions = {
  safety_risk: { type: "noul", instructions: "Does the deviation describe a credible risk to a person's safety, system safety, or safe operation?", criteria: { true: "A stated condition can cause injury, unsafe operation, or loss of a safety control", false: "No concrete safety consequence is described" } },
  compliance_risk: { type: "noul", instructions: "Does the deviation credibly indicate a compliance, regulatory, qualification, authorization, or audit-evidence breach?", criteria: { true: "A mandatory rule, approval, qualification, authorization, or evidence requirement may be violated", false: "The deviation is operational only and does not indicate such a breach" } },
  delivery_blocked: { type: "noul", instructions: "Does the deviation prevent the service item from safely continuing or being delivered without human intervention?", criteria: { true: "Work cannot responsibly proceed or deliver until someone resolves the issue", false: "Work can continue under the documented process" } },
  severity_understated: { type: "noul", instructions: "Is the reported severity likely lower than the consequences described in the deviation?", criteria: { true: "The narrative supports a materially more severe classification than `reported_severity`", false: "The reported severity is at least as high as the described consequences" } },
};

async function evaluate(state) {
  const response = await fetch(endpoint, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, "Content-Type": "application/json" },
    body: JSON.stringify({ state, model: "jev-latest", questions }),
  });
  if (!response.ok) throw new Error(`TypeSafe returned HTTP ${response.status}`);
  const body = await response.json();
  const values = {};
  for (const key of Object.keys(questions)) {
    const value = body?.answers?.[key]?.noul;
    if (typeof value !== "number" || value < 0 || value > 1) throw new Error(`invalid ${key} response`);
    values[key] = value;
  }
  return { model: body.model, values };
}

const samples = [];
for (const fixture of fixtures) {
  const results = [];
  for (let run = 0; run < runs; run += 1) results.push(await evaluate(fixture.state));
  const probabilities = {};
  const spread = {};
  for (const key of Object.keys(questions)) {
    const values = results.map((result) => result.values[key]);
    probabilities[key] = values.reduce((sum, value) => sum + value, 0) / values.length;
    spread[key] = Math.max(...values) - Math.min(...values);
  }
  samples.push({ id: fixture.id, source: fixture.source, labels: fixture.labels, probabilities, spread });
}

const metrics = {};
for (const key of Object.keys(questions)) {
  const labeled = samples.filter((sample) => typeof sample.labels?.[key] === "boolean");
  metrics[key] = {
    count: labeled.length,
    brier: labeled.reduce((sum, sample) => sum + (sample.probabilities[key] - Number(sample.labels[key])) ** 2, 0) / Math.max(1, labeled.length),
    accuracy_at_0_5: labeled.filter((sample) => (sample.probabilities[key] >= 0.5) === sample.labels[key]).length / Math.max(1, labeled.length),
    max_repeat_spread: Math.max(0, ...labeled.map((sample) => sample.spread[key])),
  };
}
const historicalCount = samples.filter((sample) => sample.source === "historical").length;
const calibration = historicalCount >= 30
  ? { status: "ELIGIBLE_FOR_REVIEW", reason: "At least 30 labeled historical cases are present; threshold selection still requires reviewer sign-off." }
  : { status: "NOT_CALIBRATED", reason: `Only ${historicalCount} labeled historical cases are present; monitoring ranking must remain disconnected.` };
const report = `${JSON.stringify({ generated_at: new Date().toISOString(), runs, sample_count: samples.length, historical_count: historicalCount, metrics, calibration, samples }, null, 2)}\n`;
if (process.env.TYPESAFE_EVAL_OUTPUT) {
  await fs.writeFile(process.env.TYPESAFE_EVAL_OUTPUT, report, { encoding: "utf8", mode: 0o600 });
}
process.stdout.write(report);
