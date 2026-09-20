# TypeSafe local evaluation only

This directory is an offline developer/QA evaluation harness. It is not part of the Project
Management application runtime, API, configuration, dependency injection, workflow or monitoring
ranking.

Run the evaluator manually with `TYPESAFE_API_KEY` supplied only in the local process environment.
Never copy the key into project `.env` files, Compose, deployment manifests, browser code, test
fixtures or reports.

The generated report is evidence for local model evaluation only. It must not be interpreted as a
production feature or permission to send live business deviation data to TypeSafe.
