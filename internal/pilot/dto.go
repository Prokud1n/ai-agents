package pilot

// SaveFeedbackData is a /feedback of the trainer; TaskID and TaskVersion are 0 when it is not a reply to a draft.
type SaveFeedbackData struct {
	AuthorID    int64
	AuthorName  string
	Text        string
	Truncated   bool
	TaskID      int64
	TaskVersion int
	Topic       string
	TopicID     int64
	ChatID      int64
	MessageID   int64
}
