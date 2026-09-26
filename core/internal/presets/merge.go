package presets

// Merge lays one catalogue's schools over another's.
//
// The value comes from the overlay and the position comes from the base. That
// pairing is the whole rule and it is easy to get half right: taking the
// overlay's order as well would make the list reshuffle itself every time a
// refresh brought a school forward, under the cursor of somebody scrolling it.
//
// So a school that exists in both is updated where it already sat, and one that
// exists only in the overlay is appended. The base is the built-in catalogue
// that ships with the package; the overlay is whatever was cached from the
// network, which is newer but not more authoritative about ordering.
//
// Drafts are carried through. Filtering happens at the end, in Active, because
// hiding a draft and dropping it are different: dropping the remote draft of a
// school the built-in catalogue lists as active would silently reinstate the
// older entry, which is the opposite of what a draft update means.
func Merge(base, overlay []School) []School {
	position := make(map[string]int, len(base))
	merged := make([]School, 0, len(base)+len(overlay))

	for _, school := range base {
		if _, seen := position[school.ShortName]; seen {
			continue
		}
		position[school.ShortName] = len(merged)
		merged = append(merged, school)
	}

	for _, school := range overlay {
		if at, seen := position[school.ShortName]; seen {
			merged[at] = school
			continue
		}
		position[school.ShortName] = len(merged)
		merged = append(merged, school)
	}
	return merged
}

// Find returns one school by its identifier.
//
// The identifier is the normalised one, so a caller holding what a user typed
// has to put it through the same reduction first -- which is why that is
// exported as SafeID rather than left inside the parser.
func Find(schools []School, shortName string) (School, bool) {
	wanted := SafeID(shortName)
	for _, school := range schools {
		if school.ShortName == wanted {
			return school, true
		}
	}
	return School{}, false
}
