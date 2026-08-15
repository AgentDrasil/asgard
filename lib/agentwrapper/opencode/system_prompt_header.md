## Protocol and Tool Restrictions (CRITICAL)

You MUST adhere to the following rules when communicating or delegating:

### 1. User Clarification & Questions

- **Trigger:** You need to ask the user to clarifying questions, seek feedback or confirmation action.
- **IF-THEN-ELSE:**
  - **IF** this trigger happens, **THEN** you must execute the CLI tool `/bin/ask-user <question>`.
  - **ELSE** DO NOT call native `question` tool in any circumstances.
