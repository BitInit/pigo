package ai

// CloneMessage copies built-in message payloads, normalizing pointers to values.
// Custom messages and content blocks are returned unchanged and must be immutable.
func CloneMessage(message Message) Message {
	switch m := message.(type) {
	case *SystemMessage:
		if m != nil {
			return CloneMessage(*m)
		}
	case *UserMessage:
		if m != nil {
			return CloneMessage(*m)
		}
	case *AssistantMessage:
		if m != nil {
			return CloneMessage(*m)
		}
	case *ToolResultMessage:
		if m != nil {
			return CloneMessage(*m)
		}
	case SystemMessage:
		return m
	case UserMessage:
		m.Content = cloneContent(m.Content)
		return m
	case AssistantMessage:
		m.Content = cloneContent(m.Content)
		m.ProviderData = append(m.ProviderData[:0:0], m.ProviderData...)
		m.Usage = cloneUsage(m.Usage)
		return m
	case ToolResultMessage:
		m.Content = cloneContent(m.Content)
		m.Details = append(m.Details[:0:0], m.Details...)
		if m.Usage != nil {
			usage := cloneUsage(*m.Usage)
			m.Usage = &usage
		}
		return m
	default:
		return message
	}
	return nil
}

// CloneMessages copies a transcript using CloneMessage's ownership rules.
func CloneMessages(messages []Message) []Message {
	result := make([]Message, len(messages))
	for i, message := range messages {
		result[i] = CloneMessage(message)
	}
	return result
}

func cloneUsage(usage Usage) Usage {
	if usage.Reasoning != nil {
		n := *usage.Reasoning
		usage.Reasoning = &n
	}
	if usage.Cost != nil {
		cost := *usage.Cost
		usage.Cost = &cost
	}
	return usage
}

func cloneContent(blocks []ContentBlock) []ContentBlock {
	result := make([]ContentBlock, len(blocks))
	for i, block := range blocks {
		switch b := block.(type) {
		case *TextContent:
			if b != nil {
				result[i] = *b
			}
		case *ThinkingContent:
			if b != nil {
				result[i] = *b
			}
		case *ImageContent:
			if b != nil {
				result[i] = *b
			}
		case *ToolCall:
			if b != nil {
				c := *b
				c.Arguments = append(c.Arguments[:0:0], c.Arguments...)
				result[i] = c
			}
		case ToolCall:
			b.Arguments = append(b.Arguments[:0:0], b.Arguments...)
			result[i] = b
		default:
			result[i] = block
		}
	}
	return result
}
