## ADDED Requirements

### Requirement: Production deploys state their task count
Production synthesis SHALL require `productionTaskCount`, a whole number from 0 to 4, and the production service template SHALL carry that count. Synthesis SHALL fail when the value is missing or invalid.

#### Scenario: Deploy without a task count
- **WHEN** production is synthesized without `productionTaskCount`
- **THEN** synthesis fails and nothing is deployed

#### Scenario: Ordinary production deploy
- **WHEN** production runs 2 tasks and a release deploys with `productionTaskCount=2`
- **THEN** the service keeps 2 tasks throughout the deploy

### Requirement: Staging deploys keep their task count
The staging service template SHALL name no task count, so a deploy leaves the running count unchanged.

#### Scenario: Staging scaled for a rehearsal
- **WHEN** staging runs 2 tasks and the stack is deployed
- **THEN** staging still runs 2 tasks afterwards

### Requirement: Alarm definitions are stable across synths
Each alarm's metric math SHALL list its metrics in a fixed order.

#### Scenario: Deploy with no alarm change
- **WHEN** the stack is synthesized twice from the same source
- **THEN** every alarm's metrics appear in the same order both times
