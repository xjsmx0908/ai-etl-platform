# Historical Evaluation Data Intake

Use this process to prepare the private 100-case production evaluation set.
Do not add the finished dataset to Git. Store it as
`docs/evals/private/historical-golden-set.json`, which is ignored locally, or
in an approved protected data store.

## Required Case Fields

Each case requires `id`, `filename`, `permission`, `content`, `query`, and
`reference_answer`. `content` is the deidentified evidence that should be
uploaded for the test; `reference_answer` is the reviewer-approved expected
answer. Preserve the optional retrieval assertions supported by
`golden-set.json` when they represent a real requirement.

Choose cases from real user intent distributions rather than repeating one
document type. Include semantic questions, exact identifiers, no-answer
questions, permission boundaries, stale or conflicting documents, and common
failure modes.

## Deidentification

Remove or replace names, emails, phone numbers, identity numbers, account
numbers, credentials, webhook URLs, customer identifiers, and internal URLs.
Stable placeholders such as `customer-a`, `order-0001`, and `region-x` are
appropriate when relationships must remain testable. Keep the mapping from
original data to placeholders outside this repository.

The validator detects only high-confidence patterns. Passing it is necessary,
but it does not prove that the dataset is safe to share or send to an external
Judge model. Data owners remain responsible for review and approval.

## Preflight And Run

Copy the empty template into the ignored directory, populate at least 100
approved cases, then run:

```bash
cp docs/evals/historical-golden-set.template.json \
  docs/evals/private/historical-golden-set.json

python3 scripts/validate_eval_dataset.py \
  --golden-set docs/evals/private/historical-golden-set.json
```

Run the complete deterministic and Judge evaluation only in a dedicated,
approved environment. `run-evals.py` now creates its own Compose project and
uses random host ports by default, so cleanup cannot stop the default local
development stack. Use an enterprise-approved Judge endpoint and retain the
generated report as sensitive material:

```bash
JUDGE_API_KEY=... JUDGE_ENDPOINT=... JUDGE_MODEL=... \
python3 scripts/run-evals.py \
  --golden-set docs/evals/private/historical-golden-set.json \
  --judge \
  --judge-min-pass-rate 0.80 \
  --judge-min-faithfulness 4.0
```
