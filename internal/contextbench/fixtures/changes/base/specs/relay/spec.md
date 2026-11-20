## ADDED Requirements

### Requirement: Unticking a chat stops new messages
The app SHALL stop sending new messages from a chat as soon as the user unticks it.
**Scope fence:** Messages already sent to the CRM stay there.
**Failure:** If the app is offline when the chat is unticked, queued messages from it are dropped, not sent later.
**Visual:** chat-picker screen in ui-preview.html.

#### Scenario: User unticks a chat with messages waiting
- **WHEN** the user unticks a chat that has 3 messages waiting in the outbox
- **THEN** those 3 messages never reach the CRM
- **AND** messages from that chat already in the CRM stay there

## REMOVED Requirements

### Requirement: Send everything
**Reason:** Sending every chat leaked private chats.
**Migration:** Users tick the chats they want sent.
**Effect on existing data:** Messages already sent stay in the CRM.
