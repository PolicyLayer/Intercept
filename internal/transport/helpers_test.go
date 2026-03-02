package transport

func PassthroughHandler(_ ToolCallRequest) ToolCallResult {
	return ToolCallResult{Handled: false}
}
