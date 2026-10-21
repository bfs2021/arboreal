package arboreal

import "github.com/zenful-ai/arboreal/llm"

// TrailingUserMessages selects the user's messages since the agent last
// spoke: every user-role message after the transcript's last assistant-role
// message, in order, as a new list of shallow copies whose annotation maps
// stay shared with the transcript. It selects nothing when no user message
// follows the last assistant message. It is the Intake for a tree that
// answers a thread, where several people may reply before the agent speaks
// again.
//
// "Since the agent last spoke" is measured on the transcript. An executive
// that failed a turn and kept its paused steps has already given those
// steps that turn's messages, and a second turn on the same executive
// selects them again; a turn retried from the snapshot taken before the
// failure does not.
func TrailingUserMessages(transcript AnnotatedMessages) AnnotatedMessages {
	start := 0
	for i := len(transcript) - 1; i >= 0; i-- {
		if transcript[i].Role == llm.ChatMessageRoleAssistant {
			start = i + 1
			break
		}
	}

	var out AnnotatedMessages
	for _, m := range transcript[start:] {
		if m.Role == llm.ChatMessageRoleUser {
			out = append(out, m)
		}
	}
	return out
}

// LastUserMessage selects the newest user-role message since the agent last
// spoke, or nothing. It is a BehaviorTree's Intake when none is set.
func LastUserMessage(transcript AnnotatedMessages) AnnotatedMessages {
	trailing := TrailingUserMessages(transcript)
	if len(trailing) == 0 {
		return nil
	}
	return trailing[len(trailing)-1:]
}
