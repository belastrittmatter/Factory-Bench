## ADDED Requirements

### Requirement: Unticking a chat stops new messages
The app SHALL stop sending new messages from a chat as soon as the user unticks it.

#### Scenario: User unticks a chat
- **WHEN** the user unticks a chat with 3 messages waiting
- **THEN** those 3 messages never reach the CRM
