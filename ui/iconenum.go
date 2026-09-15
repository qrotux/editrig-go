package ui

// IconEnumFieldKey is the "ui:field" value of an enum with previews. It
// mirrors the client's field registry key - the same cross-language
// convention as IconFieldKey and MediaFieldKey.
const IconEnumFieldKey = "iconEnum"

// IconEnum is an enumeration whose values are shown as images: the form
// draws a searchable preview grid instead of a select. previews maps value
// to image URL; it goes in ui:options because the client must not know where
// assets live. On the wire the property stays a plain enum - validation,
// labels (EnumLabels) and Nullable work as for Enum. Lint checks that
// previews agree with the enum values.
func IconEnum(previews map[string]string, values ...string) Field {
	f := Enum(values...)
	f["ui:field"] = IconEnumFieldKey
	f["ui:options"] = map[string]any{"previews": previews}
	return f
}
