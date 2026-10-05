# Job: stabilize script output

## Objective
Make Port Peek useful in scripts without requiring text scraping.

## Tasks
- Add a versioned JSON output format that represents multiple matches and unavailable fields.
- Define stable exit codes for match, no match, bad input, and inspection failure.
- Test JSON and text output against shared fixtures on each supported platform.
- Document compatibility expectations for future fields.

## Expected outcome
Scripts can distinguish success, absence, and failure and consume a documented JSON schema.
