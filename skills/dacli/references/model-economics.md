# Model routing and usage

The host executes coding turns and enforces its spending controls. When separately
enabled by the owner, dacli ranks permitted role/model candidates using task
difficulty, capabilities, current availability and bounded correction policy.
A controller's `get_dispatch_routing` read returns the selected next-turn role,
model and explanation for an exact dispatch or correction operation. Inspect
the selected controller catalog first; basic worker credentials do not expose it.
The selection is a historical explanation, not a launch permit, provider quota
guarantee or permission to change a running agent's model. Follow the user's
model choices within the configured eligible routes.

Recorded budget reservations and owner estimates are separate from measured
provider usage. Native usage adapters retain only attributable supported output
measurements; unsupported host callbacks remain unavailable. Do not infer tokens
from text length, enabled skills or process counts. Unknown usage and cost stay
unknown, and token observations do not prove billing enforcement. Historical
calibration can inform planning without establishing today's prices or capacity.
A model name never proves authority or reviewer independence.

See the [model routing contract](https://github.com/dacharika/dacli/blob/main/docs/GOVERNANCE_NATIVE_MCP.md#version-050-package-and-routing-read),
[operator playbook](https://github.com/dacharika/dacli/blob/main/docs/OPERATOR_PLAYBOOK.md)
and the selected runtime's capability catalog.
