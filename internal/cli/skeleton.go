package cli

type UpdateData struct{ Action string }
type DoctorData struct{ Checks []Check }
type Check struct{ Name, Result, Detail, Fix string }

func fsTypeOf(string) (string, error) { return "", nil }
