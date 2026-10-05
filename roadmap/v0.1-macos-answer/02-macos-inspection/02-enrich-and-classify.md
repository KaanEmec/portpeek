# Job: enrich and classify the answer

## Objective
Give the user enough process context to recognize the owner and understand its network exposure.

## Tasks
- Resolve the process name, launch command, and working directory when macOS permissions permit.
- Classify loopback, wildcard, and specific interface addresses from the bound address; avoid claims about firewalls or internet reachability.
- Preserve multiple matches and clearly identify fields that could not be read.
- Check cases where the process exits between socket lookup and enrichment.

## Expected outcome
The answer identifies the likely project/process and accurately says whether the binding is loopback-only or on another local interface.
