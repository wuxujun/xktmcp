package model

type Student struct {
	ID         uint   `json:"id"`
	Stuid      uint   `json:"stuid"`
	SmpId      string `json:"smp_id"`
	StuName    string `json:"stu_name"`
	StuNameEn  string `json:"stu_name_en"`
	Gender     string `json:"gender"`
	Grade      string `json:"grade"`
	SchoolName string `json:"school_name"`
	StuStatus  string `json:"stu_status"`
	Userid     string `json:"userid"`
	Uaid       string `json:"uaid"`
}

type StudentOrder struct {
	ID     uint          `json:"id"`
	Stuid  uint          `json:"stuid"`
	SmpId  string        `json:"smp_id"`
	Orders []interface{} `json:"orders"`
}

type StudentExam struct {
	ID    uint          `json:"id"`
	Stuid uint          `json:"stuid"`
	SmpId string        `json:"smp_id"`
	Exams []interface{} `json:"exams"`
}
