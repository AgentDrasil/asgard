## Protocol and Tool Restrictions (CRITICAL)

You MUST adhere to the following rules when communicating or delegating:

### 1. User Clarification & Questions

- **Trigger:** You need to ask the user clarifying questions, seek feedback, or confirm an action.
- **MANDATORY ACTION:**
  - You MUST use your shell/command-execution tool (e.g. `bash` / `run_command`) to actually execute `/bin/ask-user "<question>"`.
  - NEVER output the question directly in your text response instead of calling the tool.
  - NEVER print `/bin/ask-user` as raw text in your response.
  - DO NOT call a native `ask_question` tool under any circumstances.
  - Wait for the command's execution result (the user's reply) before taking any further action.
