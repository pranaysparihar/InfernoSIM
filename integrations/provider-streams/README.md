# Independent provider SDK consumers

Run `bash scripts/provider-stream-smoke.sh` from the repository root. It creates
a temporary Python environment with pinned official OpenAI, Anthropic and Ollama
SDKs, exports baseline and mutated streams through InfernoSIM's actual fault
engine, and serves them over loopback HTTP. No API key or live model is required.

The SDKs accumulate the result independently of `pkg/toolstream`. All four
formats run with baseline and a mutation of only `call_b` (or Ollama index 1),
using server writes of 1, 17 and 4096 bytes: 24 checks. TCP can coalesce writes;
the test does not claim to control network packet boundaries. The checks require
both call identities where the protocol supplies IDs, unchanged first-call
arguments, and the expected second-call arguments.

Set `INFERNOSIM_PROVIDER_SDK_REPORT` to retain JSON results, or
`INFERNOSIM_PROVIDER_PYTHON` to use an existing environment. Results record the
actual SDK versions. The default requirements pin all Python dependencies.
This is a recorded-stream consumer compatibility check, not validation against
live provider services or every historical SDK version.

Primary SDK sources: [OpenAI](https://github.com/openai/openai-python),
[Anthropic](https://github.com/anthropics/anthropic-sdk-python),
[Ollama](https://github.com/ollama/ollama-python).
