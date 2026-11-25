package roughcut

// TargetMargin over the target length of a reel: a cleaned talking head up to this much longer (66 s for 60 s) is
// left as it is, without the editor's selection — a few seconds over are not worth a model call.
const TargetMargin = 1.1

// LongerThanTarget reports whether a cleaned talking head of the duration needs the editor's selection for a reel of
// the target length (seconds; 0 turns it off). The media worker stops such an auto job for the editor and the app
// asks the model for the selection by the same rule.
func LongerThanTarget(duration, target float64) bool {
	return target > 0 && duration > target*TargetMargin
}
