package main

import (
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Filter struct { User,Search,Kind,Status,Sort,Mode string; From,To time.Time; Page,Size int }
type Totals struct {
	Plays int `json:"plays"`
	Contents int `json:"contents"`
	Seconds int64 `json:"seconds"`
	Completed int `json:"completed"`
	Open int `json:"open"`
}
type Bucket struct { Date string `json:"date"`; Plays int `json:"plays"`; Seconds int64 `json:"seconds"` }
type Ranking struct { Key string `json:"key"`; Name string `json:"name"`; Plays int `json:"plays"`; Seconds int64 `json:"seconds"` }
type Content struct {
	ID string `json:"id"`
	ItemID string `json:"itemId"`
	SeriesID string `json:"seriesId,omitempty"`
	Title string `json:"title"`
	Kind string `json:"kind"`
	Year int `json:"year,omitempty"`
	Plays int `json:"plays"`
	Seconds int64 `json:"seconds"`
	Completed int `json:"completed"`
	LastAt time.Time `json:"lastAt"`
}
type Overview struct {
	At time.Time `json:"at"`
	TimeZone string `json:"timeZone"`
	From string `json:"from"`
	To string `json:"to"`
	Days int `json:"days"`
	Stats Totals `json:"stats"`
	Today Totals `json:"today"`
	OpenCount int `json:"openCount"`
	Active []Record `json:"active"`
	Recent []Record `json:"recent"`
	Daily []Bucket `json:"daily"`
	Users []Ranking `json:"users"`
	Popular []Content `json:"popular"`
	Kinds map[string]int `json:"kinds"`
}
func matchesUser(r Record,user string) bool {return user==""||r.key()==user||r.UserID==user||strings.EqualFold(r.UserName,user)}
func contentKey(r Record) string {if r.ItemID!=""{return "id:"+r.ItemID};return "title:"+r.title()}
func totals(rows []Record) Totals {
	out:=Totals{};items:=map[string]bool{}
	for _,r:=range rows{out.Plays++;out.Seconds+=r.DurationSeconds;items[contentKey(r)]=true;if r.Completed{out.Completed++};if r.EndedAt==nil{out.Open++}}
	out.Contents=len(items);return out
}
func sortRecords(rows []Record,by string) {
	sort.Slice(rows,func(i,j int)bool{a,b:=rows[i],rows[j];if by=="duration"&&a.DurationSeconds!=b.DurationSeconds{return a.DurationSeconds>b.DurationSeconds};if !a.StartedAt.Equal(b.StartedAt){if by=="oldest"{return a.StartedAt.Before(b.StartedAt)};return a.StartedAt.After(b.StartedAt)};return a.ID<b.ID})
}
func groupContents(rows []Record,by string) []Content {
	groups:=map[string]Content{}
	for _,r:=range rows{key:=contentKey(r);g,ok:=groups[key];g.Plays++;g.Seconds+=r.DurationSeconds;if r.Completed{g.Completed++}
		if !ok||r.StartedAt.After(g.LastAt)||(r.StartedAt.Equal(g.LastAt)&&r.ID<g.ID){g.ID=r.ID;g.ItemID=r.ItemID;g.SeriesID=r.SeriesID;g.Title=r.title();g.Kind=r.ItemType;g.Year=r.Year;g.LastAt=r.StartedAt};groups[key]=g}
	out:=make([]Content,0,len(groups));for _,g:=range groups{out=append(out,g)}
	sort.Slice(out,func(i,j int)bool{a,b:=out[i],out[j];if by=="count"&&a.Plays!=b.Plays{return a.Plays>b.Plays};if by=="duration"&&a.Seconds!=b.Seconds{return a.Seconds>b.Seconds};if !a.LastAt.Equal(b.LastAt){if by=="oldest"{return a.LastAt.Before(b.LastAt)};return a.LastAt.After(b.LastAt)};return a.ID<b.ID});return out
}
func makeOverview(rows []Record,now time.Time,zone *time.Location,user string,days int) Overview {
	local:=now.In(zone);today:=time.Date(local.Year(),local.Month(),local.Day(),0,0,0,0,zone);start:=today.AddDate(0,0,1-days)
	out:=Overview{At:now,TimeZone:zone.String(),From:start.Format("2006-01-02"),To:today.Format("2006-01-02"),Days:days,Active:[]Record{},Recent:[]Record{},Daily:make([]Bucket,days),Users:[]Ranking{},Popular:[]Content{},Kinds:map[string]int{}}
	indices:=map[string]int{};for i:=range out.Daily{date:=start.AddDate(0,0,i).Format("2006-01-02");indices[date]=i;out.Daily[i].Date=date}
	selected:=[]Record{};todayRows:=[]Record{};users:=map[string]Ranking{}
	sortRecords(rows,"recent")
	for _,r:=range rows{
		if !matchesUser(r,user)||r.StartedAt.After(now){continue}
		if r.EndedAt==nil{out.OpenCount++;if len(out.Active)<6{out.Active=append(out.Active,r)}}
		i,ok:=indices[r.StartedAt.In(zone).Format("2006-01-02")];if !ok{continue}
		selected=append(selected,r);if i==days-1{todayRows=append(todayRows,r)}
		out.Daily[i].Plays++;out.Daily[i].Seconds+=r.DurationSeconds;kind:=r.ItemType;if kind!="Movie"&&kind!="Episode"{kind="Other"};out.Kinds[kind]++
		u:=users[r.key()];u.Key=r.key();u.Name=first(r.UserName,"未知用户");u.Plays++;u.Seconds+=r.DurationSeconds;users[u.Key]=u
	}
	out.Stats=totals(selected);out.Today=totals(todayRows);out.Recent=selected[:min(8,len(selected))]
	for _,u:=range users{out.Users=append(out.Users,u)}
	sort.Slice(out.Users,func(i,j int)bool{a,b:=out.Users[i],out.Users[j];if a.Seconds!=b.Seconds{return a.Seconds>b.Seconds};return a.Key<b.Key});out.Users=out.Users[:min(6,len(out.Users))]
	out.Popular=groupContents(selected,"count");out.Popular=out.Popular[:min(6,len(out.Popular))];return out
}
func dateBoundary(value string,end bool,zone *time.Location) (time.Time,error) {
	if value==""{return time.Time{},nil};t,err:=time.ParseInLocation("2006-01-02",value,zone);if err!=nil{return t,errors.New("日期格式应为 YYYY-MM-DD")};if end{t=t.AddDate(0,0,1).Add(-time.Nanosecond)};return t,nil
}
func parseFilter(q url.Values,zone *time.Location) (Filter,error) {
	f:=Filter{User:q.Get("user"),Search:strings.TrimSpace(q.Get("q")),Kind:q.Get("type"),Status:q.Get("status"),Sort:first(q.Get("sort"),"recent"),Mode:first(q.Get("mode"),"records"),Page:1,Size:20}
	if f.Mode!="records"&&f.Mode!="items"{return f,errors.New("不支持的记录视图")}
	if f.Kind!=""&&f.Kind!="Movie"&&f.Kind!="Episode"&&f.Kind!="Other"{return f,errors.New("不支持的内容类型")}
	if f.Status!=""&&f.Status!="completed"&&f.Status!="incomplete"&&f.Status!="open"{return f,errors.New("不支持的会话状态")}
	if f.Sort!="recent"&&f.Sort!="oldest"&&f.Sort!="duration"&&!(f.Sort=="count"&&f.Mode=="items"){return f,errors.New("不支持的排序方式")}
	var err error;f.From,err=dateBoundary(q.Get("from"),false,zone);if err!=nil{return f,err};f.To,err=dateBoundary(q.Get("to"),true,zone);if err!=nil{return f,err}
	if !f.From.IsZero()&&!f.To.IsZero()&&f.From.After(f.To){return f,errors.New("开始日期不能晚于结束日期")}
	if value:=q.Get("page");value!=""{f.Page,err=strconv.Atoi(value);if err!=nil||f.Page<1||f.Page>1000000{return f,errors.New("页码无效")}}
	return f,nil
}
func filterRecords(rows []Record,f Filter) []Record {
	out:=[]Record{}
	for _,r:=range rows {
		if !matchesUser(r,f.User)||(!f.From.IsZero()&&r.StartedAt.Before(f.From))||(!f.To.IsZero()&&r.StartedAt.After(f.To)){continue}
		kind:=r.ItemType;if kind!="Movie"&&kind!="Episode"{kind="Other"};if f.Kind!=""&&f.Kind!=kind{continue}
		if f.Status=="completed"&&!r.Completed||f.Status=="incomplete"&&(r.EndedAt==nil||r.Completed)||f.Status=="open"&&r.EndedAt!=nil{continue}
		if f.Search!=""&&!strings.Contains(strings.ToLower(strings.Join([]string{r.title(),r.UserName,r.DeviceName,r.Client}," ")),strings.ToLower(f.Search)){continue}
		out=append(out,r)
	};return out
}
