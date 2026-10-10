# Commercial runtime enforcement

The API and SLA notifier bind to `project_management` in compiled code. The
production Agent delivers a distinct `runtime/license-<compose-service>.env`
for each approved component; never copy credentials between services. Signed
state is persisted in that service's `/var/lib/commercial-license` volume and
the platform verification key is mounted read-only. Vendor trust is compiled
into the reviewed `third_party/license-core` consumer; no signing key is shipped.

Legacy deployments default to compatibility mode until explicitly enrolled.
When `COMMERCIAL_LICENSE_ENABLED=true`, incomplete bindings fail startup;
missing/invalid signed state denies nonessential operations. A polling failure
does not extend the signed absolute expiry. Existing permission and data-scope
checks remain in force. Configuration and credential files are privileged
deployment assets, not an anti-root security boundary.

After expiry new project mutations, uploads and SLA notification generation
stop. Finite registered historical queries and exports remain available;
unknown routes default to business mutation. Existing notification outbox
delivery and authentication/diagnostics remain technical essential work.

Run `sh scripts/license-core-sync.sh --check` before build. Independent checkouts
verify their exact mirror and checksum; workspaces additionally detect drift
from the authoritative core. Production approval requires a real immutable
image configuration digest, protocol label `com.basic-platform.license.protocol=1`
and a nonempty version built with `--build-arg APP_VERSION=<reviewed-version>`.
Health/readiness alone is not migration eligibility or release approval.
