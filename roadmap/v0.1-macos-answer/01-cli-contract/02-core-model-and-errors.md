# Job: shared model and errors

## Objective
Keep platform discovery separate from the CLI so new operating systems do not change the product contract.

## Tasks
- Create small socket, process, and inspection-result types with explicit optional fields.
- Define adapter boundaries for listing local matches and enriching process details.
- Normalize command failures, missing permissions, and absent data into user-facing errors.
- Test formatting and error mapping with fixtures that do not depend on a live process.

## Expected outcome
The CLI can render fixture results consistently, and platform adapters have a narrow, testable interface.
