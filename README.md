# crucible

An open-source agent that tries to reproduce the headline results of published computational papers
inside a sandbox, and publishes an open, **rerunnable** reproducibility record for each attempt.

Science can't correct itself if nobody checks it. Checking takes a skilled person days per paper, so
almost nobody does. `crucible` makes a first check cheap, and makes the evidence public and replayable.

> Status: early. The full pipeline is implemented and unit-tested; accuracy on real papers has not yet
> been measured. Don't treat any verdict as final without reading the transcript.

## How it works

```
arXiv id → ingest → claims → sandbox + agent → compare → record → registry
```

1. **ingest** – fetches the paper's LaTeX from arXiv, finds the GitHub repo, pins the commit.
2. **claims** – an LLM pulls out the headline numbers from the paper's tables (and the best baseline the
   paper itself reports).
3. **agent** – an LLM builds the environment and finds commands that print those numbers, inside a
   locked-down Docker container (no host mounts, all capabilities dropped, CPU/memory/process limits).
4. **compare** – **the model never reports numbers.** The harness re-runs the chosen commands itself and
   extracts values with a regex, so results can't be hallucinated. Verdicts: `reproduced`, `partial`,
   `not_reproduced`, `could_not_run` (with a reason).
5. **record** – verdicts, the exact commands, a Dockerfile and the agent transcript. `crucible rerun` replays
   the record with **no LLM at all**.
6. **registry** – a static site with a page and a README badge per paper.

## Quick start

Requires Go 1.22+, Docker, and an OpenAI-compatible model endpoint (local [Ollama](https://ollama.com)
works; so do OpenRouter, Groq, vLLM...).

```sh
go build -o crucible ./cmd/crucible

crucible discover --limit 50 --triage        # find time-series forecasting papers with runnable code
crucible run 2610.25392                      # attempt one paper -> records/2610.25392/
crucible batch --candidates candidates.json  # attempt many, resumable
crucible rerun 2610.25392                    # replay a record without an LLM
crucible review 2610.25392 --approve         # human sign-off (required before negatives are published)
crucible site --out public                   # build the registry and badges
```

Pick a model with `--base-url`, `--model`, `--api-key` (or `CRUCIBLE_BASE_URL`, `CRUCIBLE_MODEL`,
`CRUCIBLE_API_KEY`). Small local models can extract claims but are weak at running the agent loop; a larger
open model on a hosted endpoint works much better.

```sh
export CRUCIBLE_BASE_URL=https://openrouter.ai/api/v1
export CRUCIBLE_MODEL=qwen/qwen3-coder
export CRUCIBLE_API_KEY=...
```

## Measuring the agent

`crucible eval --tasks tasks.json` runs the agent on tasks with known answers and reports accuracy, so
claims about the tool rest on numbers. Task format (`claims[].value` is the ground truth):

```json
[{
  "id": "example-1",
  "title": "Example capsule",
  "repo_url": "https://github.com/owner/repo",
  "commit": "optional-sha",
  "tolerance": 0.02,
  "claims": [{"id": "c1", "metric": "accuracy", "dataset": "test", "value": 0.913, "source": "answer key"}]
}]
```

Converting [CORE-Bench](https://arxiv.org/abs/2409.11363) tasks into this format is left as a script
to write; it is not included yet.

## Safety and fairness

- A negative result (`not_reproduced`, `could_not_run`) is **not** a claim about the authors. The failure may
  be the agent's, the sandbox's, or a missing resource. Negative records are never published until a human
  runs `crucible review --approve`.
- Every published page links to a dispute form.
- The tool never opens issues or contacts authors by itself.
- The sandbox currently has open outbound network access (needed for `git clone` and `pip install`).
  Egress filtering is not implemented, so run it on a machine you're comfortable exposing to the code of
  arbitrary research repositories.

## Roadmap

- Egress allow-listing for the sandbox.
- A CORE-Bench converter and published accuracy numbers per model.
- GPU support, and other fields (tabular ML, replication packages in R).

License: Apache-2.0. See [CONTRIBUTING.md](CONTRIBUTING.md).
