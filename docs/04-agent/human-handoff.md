# Human Handoff Feature

## Overview

The Human Handoff feature allows the Agent to proactively request human intervention when it encounters situations requiring human judgment, credential input, or operations beyond its capabilities.

This feature is inspired by the `take_over` mechanism in the [Open-AutoGLM](https://github.com/zai-org/Open-AutoGLM) project, but adopts a **non-blocking conversational flow design** that is better suited for continuous voice conversation scenarios.

## Core Design

Unlike traditional blocking callbacks, this implementation uses a **natural conversation flow**:

1. Agent calls the `request_human_handoff` tool
2. **Tool returns immediately**, telling the Agent what to notify the user about
3. Agent tells the user what needs to be done via TTS or text
4. **Conversation turn ends**, waiting for the user's next message
5. User completes the task and says "done"/"continue" etc.
6. **New conversation turn begins**, Agent takes a screenshot to verify and continues execution

This is as natural as human-to-human conversation:
```
User: "Help me log into App Store"
Agent: "I need your help. Please enter your Apple ID password on the screen, then tap login. Tell me to continue when you're done."
[User enters password]
User: "Done"
Agent: [Takes screenshot] "Good, I see login was successful. Now..."
```

## Use Cases

The Agent can request human handoff in the following scenarios:

### 1. **Authentication** (`authentication`)
- Login screen requires username and password
- Need to input credentials the Agent cannot access
- Multi-factor authentication required

### 2. **Verification Codes** (`captcha`, `verification_code`)
- Graphical CAPTCHA
- SMS verification code
- Email verification code
- Identity verification challenge

### 3. **Sensitive Operations** (`sensitive_operation`)
- Payment confirmation
- Bank transfer
- Deleting important data
- Permission changes

### 4. **Screen Not Visible** (`black_screen`)
- Screenshot returns black screen
- Sensitive pages (payment, banking apps) automatically block screenshots
- Cannot obtain screen content

### 5. **Ambiguous Situation** (`ambiguous_situation`)
- Uncertain which option to choose
- Requires user preference decision
- Multiple possible action paths

### 6. **Unsupported Action** (`unsupported_action`)
- Required functionality exceeds tool capabilities
- Hardware limitations
- System limitations

### 7. **Stuck** (`stuck`)
- Unable to continue after multiple attempts
- Encountered unknown error
- Abnormal interface state

### 8. **Other** (`other`)
- Other situations requiring human intervention

## Tool Usage

### Tool Name
`request_human_handoff`

### Input Format

```json
{
  "reason": "authentication",
  "details": "Login screen requires password for user@example.com",
  "suggested_action": "Please enter your password in the password field"
}
```

### Parameter Description

- **reason** (required): Category of the handoff request, see use cases above
- **details** (required): Detailed description of the current situation so humans understand what happened
- **suggested_action** (optional): Suggested action for the human to take

### Output

The tool **returns immediately** without blocking. Return format:

```
HUMAN_HANDOFF_REQUESTED. You need to ask the user to perform a manual action. 
Reason: Login screen requires password for user@example.com. 
Tell the user: "Please enter your password in the password field" 
Then ask the user to tell you when they have completed the task (e.g., say 'done', 'continue', or 'finished'). 
After the user confirms, take a screenshot to verify the current state before proceeding.
```

The Agent will understand this return value and convert it into a voice or text prompt for the user.

## Code Integration

### Simple Integration (Recommended)

No callback functions needed, the tool is automatically registered:

```go
package main

import (
    "agent/internal/agent"
)

func main() {
    // Create ToolSet directly, human_handoff tool is automatically registered
    toolSet := agent.NewBuiltinToolSet(
        hidCfg,
        audioCfg,
        searchCfg,
        proxyCfg,
    )
    
    // Verify tool is registered
    if tool, ok := toolSet.Get("request_human_handoff"); ok {
        fmt.Printf("Human handoff tool ready: %s\n", tool.Name())
    }
}
```

### Explaining in Agent Prompt

Inform the Agent in the system prompt that it can use this tool:

```
When you encounter situations that require human intervention, use the request_human_handoff tool:
- Login screens requiring passwords
- CAPTCHA or verification codes
- Sensitive operations like payments
- Black screens or inaccessible content
- Situations where you're stuck after multiple attempts

The tool returns immediately with instructions. Communicate these instructions to the user through your response,
then wait for the user to tell you they have completed the task in their next message.
After they confirm, take a screenshot to verify the result before continuing.
```

## Interaction Flow Examples

### Voice Mode

**Turn 1 - Agent requests handoff**:
```
User (voice): "Help me log into WeChat"
Agent: [Analyzes screenshot] → [Determines password needed] → [Calls request_human_handoff]
       Tool returns: "HUMAN_HANDOFF_REQUESTED. Tell user to enter password..."
Agent (TTS): "I need your help. WeChat login requires a password. Please enter your password on the screen and tap login. Tell me 'continue' when you're done."
[Conversation turn ends]
```

**User action**:
```
[User enters password on phone and taps login]
```

**Turn 2 - User confirms completion**:
```
User (voice): "Done" / "Continue" / "I've logged in"
Agent: [Takes screenshot] → [Analyzes verification]
Agent (TTS): "Great, I see login was successful. Now I'll help you..."
[Continues with subsequent tasks]
```

### Text Conversation Mode

Same flow, but using text instead of voice:

```
User: Help me open Alipay and recharge my phone
Agent: [Navigates to Alipay recharge page] → [Discovers payment password needed]
Agent: I need your help. The payment page requires your payment password to confirm recharging 50 yuan. Please enter your payment password on your phone and confirm. Tell me "continue" when you're done.
[Waits for user input]
User: Done
Agent: [Verifies with screenshot] Payment successful! Your phone has been recharged.
```

## Agent Behavior Guidelines

In the Agent's system prompt, it is recommended to include the following guidance:

```markdown
## Human Handoff Guidelines

Use the request_human_handoff tool when:

1. You need credentials you don't have (passwords, API keys, etc.)
2. You encounter CAPTCHA or verification challenges
3. The screen shows a black image or is not accessible
4. You're performing sensitive operations that need human approval
5. You've tried multiple approaches and are stuck
6. The task fundamentally requires human judgment

Always provide:
- Clear reason category
- Detailed explanation of the situation
- Specific suggestion of what the human should do

**Important**: This tool returns immediately with instructions for you to communicate.
After you tell the user what to do:
1. End your response and wait for their next message
2. When they say they're done (e.g., "continue", "done", "finished"), take a screenshot
3. Verify the result before proceeding with the task
```

## Testing

Run unit tests:

```bash
cd src/agent/internal/agent
go test -v -run TestHumanHandoff
```

## Example Scenarios

### Scenario 1: Login Requires Password

```json
{
  "tool": "request_human_handoff",
  "input": {
    "reason": "authentication",
    "details": "App Store login screen requires Apple ID password. I cannot access the user's password.",
    "suggested_action": "Please enter your Apple ID password in the password field, then tap the login button."
  }
}
```

**Agent's response flow**:
1. Calls the tool, tool returns instructions immediately
2. Agent tells the user: "I need your help. The App Store login screen requires your Apple ID password. Please enter your Apple ID password in the password field, then tap the login button. Tell me 'continue' when you're done."
3. Waits for user reply
4. After user says "done", Agent takes a screenshot to verify login status

### Scenario 2: Encountered Verification Code

```json
{
  "tool": "request_human_handoff",
  "input": {
    "reason": "captcha",
    "details": "Website displays a graphical CAPTCHA that requires identifying and entering characters from the image.",
    "suggested_action": "Please look at the CAPTCHA image on screen and enter the correct characters."
  }
}
```

**Interaction flow**:
```
Agent: "I encountered a CAPTCHA. Please look at the CAPTCHA image on screen and enter the correct characters. Tell me when you're done."
[User enters CAPTCHA]
User: "Entered it"
Agent: [Verifies with screenshot] "Okay, CAPTCHA passed, continuing..."
```

### Scenario 3: Payment Confirmation

```json
{
  "tool": "request_human_handoff",
  "input": {
    "reason": "sensitive_operation",
    "details": "About to execute payment operation, amount is ¥299. This is a sensitive operation that requires user confirmation.",
    "suggested_action": "Please check that the payment amount and recipient are correct, then confirm the payment."
  }
}
```

**Interaction flow**:
```
Agent: "I need you to confirm a payment operation. About to pay ¥299. Please check that the payment amount and recipient are correct, then confirm the payment. Tell me when you're done."
[User checks and confirms payment]
User: "Payment done"
Agent: [Verifies with screenshot] "Payment successful! Order confirmed."
```

### Scenario 4: Black Screen

```json
{
  "tool": "request_human_handoff",
  "input": {
    "reason": "black_screen",
    "details": "Screenshot returns completely black image, possibly because the banking app has enabled anti-screenshot protection.",
    "suggested_action": "Please manually complete the operations in the banking app, then notify me to continue."
  }
}
```

**Interaction flow**:
```
Agent: "I cannot see the screen content, possibly because the banking app has enabled anti-screenshot protection. Please manually complete the operations in the banking app, then notify me to continue."
[User completes operations]
User: "Done"
Agent: [Attempts screenshot or executes next step]
```

## Differences from Blocking Mode

### Blocking Mode (Open-AutoGLM Original Design)
Suitable for **script-style one-off task execution**:
```python
agent.run("Buy a copy of 'The Three-Body Problem' on JD.com")
# → Encounters login
# → Tool call blocks
# → User presses Enter
# → Continues execution
# → Completes purchase
```

### Non-Blocking Mode (This Implementation)
Suitable for **continuous voice/text conversation**:
```
User: "Buy a copy of 'The Three-Body Problem' on JD.com"
Agent: "Okay, opening JD.com... I need your help. Please enter your JD.com account password. Tell me when you're done."
[Conversation turn ends, waits for user]
User: "Done"
[New conversation turn begins]
Agent: "Login successful. Searching for 'The Three-Body Problem'..."
```

## Advantages

1. **Natural conversation flow**: Conforms to human conversation habits
2. **Suitable for voice interaction**: Informs user via TTS, user can reply by voice
3. **No callback implementation needed**: Simplifies integration, just register the tool
4. **Clear state**: Each conversation turn is independent, no feeling of being "stuck"
5. **Easy to understand**: For users, it's like talking to a real human assistant

## Notes

1. **Verify results**: After user confirms completion, Agent must take a screenshot to verify results
2. **Prompt guidance**: Need to clearly tell Agent in system prompt how this tool works
3. **User confirmation keywords**: Agent should recognize multiple confirmation expressions ("done"/"continue"/"finished"/"OK" etc.)
4. **Timeout handling**: Should have reasonable timeout mechanism if user doesn't reply for a long time

## Related Files

- `src/agent/internal/agent/tools_human_handoff.go` - Core implementation
- `src/agent/internal/agent/tools_human_handoff_test.go` - Unit tests
- `src/agent/internal/agent/tools.go` - ToolSet integration
