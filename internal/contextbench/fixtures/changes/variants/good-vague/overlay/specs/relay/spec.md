## ADDED Requirements

### Requirement: Unticking a chat stops new messages
The app SHALL stop sending new messages from a chat as soon as the user unticks it.
**Scope fence:** Messages already sent to the CRM stay there.

#### Scenario: User unticks a chat
- **WHEN** the user unticks a chat
- **THEN** the app handles the messages properly
