package chat

type InboxFolder struct {
	Folder        string                `json:"folder"`
	TotalCount    int                   `json:"totalCount"`
	UnreadCount   int                   `json:"unreadCount"`
	Conversations []ConversationSummary `json:"conversations"`
}

type Inbox struct {
	PrimaryFolder InboxFolder          `json:"primary"`
	Folders       []InboxFolderSummary `json:"folders"`
}

type InboxFolderSummary struct {
	Folder      string `json:"folder"`
	TotalCount  int    `json:"totalCount"`
	UnreadCount int    `json:"unreadCount"`
}
