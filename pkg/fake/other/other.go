package other

type unexportedT struct{ f int }

func F() unexportedT { return unexportedT{f: 1} }
