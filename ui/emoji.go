package ui

// EmojiFieldKey is the "ui:field" value of an emoji field. It mirrors the
// client's field registry key, exactly like IconFieldKey.
const EmojiFieldKey = "emoji"

// Emoji is an emoji field: a plain string holding one emoji on the wire, a
// picker with categories and search instead of a text input in the form.
//
// A separate kind rather than Icon: Icon carries a kebab-case Lucide icon
// NAME and its list comes from an application route, while here the value is
// self-contained - the emoji set is defined by Unicode, the server has
// nothing to know about it. The cell stays a plain text column: ONLY the
// presentation changes.
func Emoji() Field {
	return Field{"type": "string", "ui:field": EmojiFieldKey}
}
