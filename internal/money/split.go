package money

func SplitEvenly(total Minor, parts int) []Minor {
	out := make([]Minor, parts)
	each := total / Minor(parts)
	for i := range out {
		out[i] = each
	}
	return out
}
