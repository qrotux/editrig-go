package ui

// GeopointFieldKey is the "ui:field" value of a geopoint field. It mirrors
// the client's field registry key (the same convention as MediaFieldKey).
const GeopointFieldKey = "geopoint"

// Geopoint is a point-on-a-map field: the renderer lives in the application,
// the core declares only the field kind.
//
// On the wire the value is the object `{"lng": <number>, "lat": <number>}` or
// null. That is NOT the storage format: the column holds a GeoJSON Point
// whose "coordinates" is the array `[lng, lat]` (longitude first, as the
// GeoJSON specification requires). Composing and decomposing between the two
// shapes is the application's job - the vocabulary only names the kind.
//
// "type" is not declared, as in JSON(): the value is an object, and rjsf
// routes an object field's renderer through ui:field, not widget
// (ObjectField ignores widgets). The key is set FIRST, before modifiers, so
// that .Readonly() sees it and does not override it with readonlyDisplay -
// the same reason as in JSON().
func Geopoint() Field {
	return Field{"ui:field": GeopointFieldKey}
}
