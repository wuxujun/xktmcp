package model

type Staff struct {
	Userid         string `json:"userid"`
	Name           string `json:"name"`
	DepartmentName string `json:"department_name"`
	CampusName     string `json:"campus_name"`
	Gender         string `json:"gender"`
	Position       string `json:"position"`
	EntryDate      string `json:"entry_date"`
}
