package roughcut

// jointEpsilon: the edges of a cut and of the kept fragments around it lie on one frame grid; a cut between two
// fragments may still differ from their edges by rounding.
const jointEpsilon = 1e-3

// WordCutJoints are the times on the timeline of the reel where a fragment of the plan starts after words were cut
// before it (a cut other than a pause between the two fragments: a take, a filler, the editor's cut): the speech
// there goes on after something said in between, unlike at a pause pressed to a breath. The subtitles start a
// sentence at such a joint when the speech recognition capitalized the word after it (T3.31).
func WordCutJoints(plan *Plan) []float64 {
	var joints []float64

	at := 0.0

	for k, iv := range plan.Keep {
		if k > 0 && wordsCutBetween(plan.Cuts, plan.Keep[k-1].End, iv.Start) {
			joints = append(joints, at)
		}

		at += iv.End - iv.Start
	}

	return joints
}

// wordsCutBetween: a cut other than a pause lies between the end of one kept fragment and the start of the next.
func wordsCutBetween(cuts []Cut, from, to float64) bool {
	for _, c := range cuts {
		if c.Reason != PauseCutReason && c.Start >= from-jointEpsilon && c.End <= to+jointEpsilon && c.End > c.Start {
			return true
		}
	}

	return false
}
