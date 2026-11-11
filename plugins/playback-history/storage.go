package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type User struct {
	Key string `json:"key"`
	ID string `json:"id,omitempty"`
	Name string `json:"name"`
	Deleted bool `json:"deleted,omitempty"`
}
type operation struct {
	Op string `json:"op"`
	ID string `json:"id,omitempty"`
	Record *Record `json:"record,omitempty"`
	User *User `json:"user,omitempty"`
}
type Store struct {
	mu sync.RWMutex
	path string
	file *os.File
	records map[string]Record
	users map[string]User
	open map[string]string
	pending map[string]time.Time
	zone *time.Location
	now func() time.Time
}

func OpenStore(path string, zone *time.Location) (*Store,error) {
	if zone==nil { zone=time.UTC }
	if err:=os.MkdirAll(filepath.Dir(path),0700);err!=nil { return nil,err }
	f,err:=os.OpenFile(path,os.O_CREATE|os.O_RDWR|os.O_APPEND,0600);if err!=nil{return nil,err}
	s:=&Store{path:path,file:f,records:map[string]Record{},users:map[string]User{},open:map[string]string{},pending:map[string]time.Time{},zone:zone,now:time.Now}
	if err=s.load();err!=nil{f.Close();return nil,err};s.reindex();return s,nil
}
func (s *Store) load() error {
	reader:=bufio.NewReader(s.file);var offset int64
	for {
		line,err:=reader.ReadBytes('\n')
		if err!=nil && err!=io.EOF {return err}
		if len(line)>4<<20{return errors.New("历史日志单行超过 4MB，未修改原文件")}
		trim:=bytes.TrimSpace(line)
		if len(trim)>0 {
			var op operation
			if decodeErr:=json.Unmarshal(trim,&op);decodeErr!=nil {
				if err==io.EOF {if e:=s.file.Truncate(offset);e!=nil{return e};return s.file.Sync()}
				return fmt.Errorf("历史文件偏移 %d 处损坏，未覆盖原文件: %w",offset,decodeErr)
			}
			switch op.Op {
			case "put": if op.Record==nil||op.Record.ID=="" {return errors.New("历史记录缺少 ID")};r:=*op.Record;r.Title="";r.Status="";r.DurationSeconds=0;s.records[r.ID]=r
			case "delete":delete(s.records,op.ID)
			case "user":if op.User!=nil {u:=*op.User;u.Key=userKey(u.ID,u.Name);s.users[u.Key]=u}
			default:return fmt.Errorf("不支持的日志操作 %q，未覆盖原文件",op.Op)
			}
		}
		offset+=int64(len(line))
		if err==io.EOF {
			if len(line)>0 && line[len(line)-1]!='\n' {if _,e:=s.file.Write([]byte{'\n'});e!=nil{return e};return s.file.Sync()};return nil
		}
	}
}
func sessionKey(r Record) string {
	item:=first(r.EventItemID,r.ItemID)
	if r.SessionID!="" {return "session:"+r.SessionID+"|"+item}
	return "user:"+r.key()+"|"+item+"|"+first(r.DeviceID,r.DeviceName)
}
func (s *Store) reindex() {
	s.open=map[string]string{}
	for id,r:=range s.records {if r.EndedAt==nil {key:=sessionKey(r);previous:=s.records[s.open[key]];if previous.ID==""||r.LastEventAt.After(previous.LastEventAt){s.open[key]=id}}}
}
func (s *Store) append(op operation, sync bool) error {
	if s.file==nil{return os.ErrClosed}
	b,err:=json.Marshal(op);if err!=nil{return err};b=append(b,'\n')
	// Roll back a partial append, so a failed write cannot corrupt the next operation.
	info,err:=s.file.Stat();if err!=nil{return err}
	if _,err=s.file.Write(b);err!=nil{_ = s.file.Truncate(info.Size());return err}
	if sync {if err=s.file.Sync();err!=nil{return err}}
	if op.Record!=nil{delete(s.pending,op.Record.ID)};if op.Op=="delete"{delete(s.pending,op.ID)}
	return nil
}
func merge(r Record,e EventData) Record {
	if e.UserName!="" {r.UserName=e.UserName};if e.UserID!=""{r.UserID=e.UserID}
	if !r.Enriched {if e.ItemName!=""{r.ItemName=e.ItemName};if e.ItemType!=""{r.ItemType=e.ItemType}}
	if e.DeviceName!=""{r.DeviceName=e.DeviceName};if e.DeviceID!=""{r.DeviceID=e.DeviceID};if e.Client!=""{r.Client=e.Client};if e.PlayMethod!=""{r.PlayMethod=e.PlayMethod}
	if e.HasPosition {r.PositionTicks=e.PositionTicks};if e.RuntimeTicks>0{r.RuntimeTicks=e.RuntimeTicks};r.LastEventAt=e.LastEventAt
	return r
}
func (s *Store) Apply(kind string,e EventData,completion int) (string,bool,error) {
	s.mu.Lock();defer s.mu.Unlock()
	if s.file==nil{return "",false,os.ErrClosed}
	key:=sessionKey(e.Record);id:=s.open[key];r,exists:=s.records[id]
	if !exists&&e.SessionID!=""&&e.ItemID!="" {
		for candidateID,c:=range s.records{if c.EndedAt==nil&&c.SessionID==e.SessionID&&c.ItemID==""{if exists{return "",false,nil};id=candidateID;r=c;exists=true}}
	}
	// Session-only progress can update exactly one matching open record.
	if !exists && e.SessionID!="" && e.ItemID=="" {
		for candidateID,c:=range s.records {if c.EndedAt==nil&&c.SessionID==e.SessionID {if exists{return "",false,nil};id=candidateID;r=c;exists=true}}
	}
	if exists && e.LastEventAt.Before(r.LastEventAt){return id,false,nil}
	if !exists && kind!="playback.started" && kind!="playback.stopped" {return "",false,nil}
	created:=!exists
	if created {
		if e.SkipNew{return "",false,nil}
		// A duplicated stop must not create another historical play.
		if kind=="playback.stopped" {for _,old:=range s.records{if old.EndedAt!=nil&&sessionKey(old)==key&&!e.LastEventAt.After(old.EndedAt.Add(30*time.Second)){return old.ID,false,nil}}}
		var b [16]byte;if _,err:=rand.Read(b[:]);err!=nil{return "",false,err};id=hex.EncodeToString(b[:])
		r=e.Record;r.ID=id;r.EventItemID=e.ItemID;r.Partial=kind=="playback.stopped"
	}
	previousKey:=sessionKey(r)
	if r.ItemID==""&&e.ItemID!=""{r.ItemID=e.ItemID;r.EventItemID=e.ItemID}
	r=merge(r,e)
	switch kind {
	case "playback.paused":r.Paused=true
	case "playback.progress":r.Paused=e.Paused
	case "playback.started":r.Paused=false
	case "playback.stopped":
		end:=e.LastEventAt;r.EndedAt=&end;r.Paused=false
		r.Completed=r.Completed||e.Finished||(r.RuntimeTicks>0&&float64(r.PositionTicks)/float64(r.RuntimeTicks)>=float64(completion)/100)
	default:return "",false,errors.New("不支持的播放事件")
	}
	if kind=="playback.progress" {
		if _,ok:=s.pending[id];!ok{s.pending[id]=s.now()}
	} else if err:=s.append(operation{Op:"put",Record:&r},true);err!=nil{return "",false,err}
	s.records[id]=r
	if exists&&previousKey!=sessionKey(r){delete(s.open,previousKey)}
	if r.EndedAt==nil{s.open[sessionKey(r)]=id}else{delete(s.open,sessionKey(r))}
	return id,created,nil
}
func (s *Store) Flush(force bool) error {s.mu.Lock();defer s.mu.Unlock();return s.flushLocked(force)}
func (s *Store) flushLocked(force bool) error {
	if s.file==nil{return os.ErrClosed};now:=s.now();written:=map[string]time.Time{}
	for id,first:=range s.pending{if !force&&now.Sub(first)<progressInterval{continue};r,ok:=s.records[id];if !ok{delete(s.pending,id);continue};if err:=s.append(operation{Op:"put",Record:&r},false);err!=nil{for savedID,at:=range written{s.pending[savedID]=at};return err};written[id]=first}
	if len(written)>0{if err:=s.file.Sync();err!=nil{for id,at:=range written{s.pending[id]=at};return err}};return nil
}
func (s *Store) Close() error {
	s.mu.Lock();defer s.mu.Unlock();if s.file==nil{return nil};err:=s.flushLocked(true);closeErr:=s.file.Close();s.file=nil;if err!=nil{return err};return closeErr
}
func (s *Store) Snapshot() ([]Record,[]User,time.Time) {
	s.mu.RLock();defer s.mu.RUnlock();now:=s.now();rows:=make([]Record,0,len(s.records));users:=map[string]User{}
	for k,u:=range s.users{users[k]=u}
	for _,r:=range s.records{rows=append(rows,r.view(now));if r.UserName!=""||r.UserID!=""{u:=users[r.key()];u.Key=r.key();u.ID=first(r.UserID,u.ID);u.Name=first(r.UserName,u.Name);users[u.Key]=u}}
	out:=make([]User,0,len(users));for _,u:=range users{out=append(out,u)}
	sort.Slice(out,func(i,j int)bool{return strings.ToLower(out[i].Name)<strings.ToLower(out[j].Name)})
	return rows,out,now
}
func (s *Store) Get(id string) (Record,bool) {s.mu.RLock();defer s.mu.RUnlock();r,ok:=s.records[id];return r,ok}
func (s *Store) PutMetadata(id string,update func(*Record)) error {
	s.mu.Lock();defer s.mu.Unlock();r,ok:=s.records[id];if !ok{return nil};update(&r)
	if err:=s.append(operation{Op:"put",Record:&r},true);err!=nil{return err};s.records[id]=r;s.reindex();return nil
}
func (s *Store) UserEvent(u User) error {
	s.mu.Lock();defer s.mu.Unlock();u.Key=userKey(u.ID,u.Name);if u.Key=="id:"{return nil}
	if err:=s.append(operation{Op:"user",User:&u},true);err!=nil{return err};s.users[u.Key]=u;return nil
}
func (s *Store) Delete(id string) (bool,error) {
	s.mu.Lock();defer s.mu.Unlock();if _,ok:=s.records[id];!ok{return false,nil}
	if err:=s.append(operation{Op:"delete",ID:id},true);err!=nil{return false,err};delete(s.records,id);s.reindex();return true,nil
}
// Rewrite commits a new snapshot before replacing in-memory state. User data is never
// removed from memory first: a full disk or failed rename leaves the old state intact.
func (s *Store) rewrite(keep map[string]Record) error {
	if s.file==nil{return os.ErrClosed}
	tmp,err:=os.CreateTemp(filepath.Dir(s.path),".history-*.tmp");if err!=nil{return err};name:=tmp.Name();defer os.Remove(name);defer tmp.Close()
	writer:=bufio.NewWriter(tmp);encoder:=json.NewEncoder(writer)
	ids:=make([]string,0,len(keep));for id:=range keep{ids=append(ids,id)};sort.Strings(ids)
	for _,id:=range ids{r:=keep[id];if err=encoder.Encode(operation{Op:"put",Record:&r});err!=nil{return err}}
	for _,u:=range s.users{if err=encoder.Encode(operation{Op:"user",User:&u});err!=nil{return err}}
	if err=writer.Flush();err!=nil{return err};if err=tmp.Sync();err!=nil{return err};if err=tmp.Close();err!=nil{return err}
	// Open the replacement before rename, so failure cannot strand a closed store.
	replacement,err:=os.OpenFile(name,os.O_RDWR|os.O_APPEND,0600);if err!=nil{return err}
	if err=os.Rename(name,s.path);err!=nil{replacement.Close();return err}
	old:=s.file;s.file=replacement;s.records=keep;clear(s.pending);s.reindex();return old.Close()
}
func (s *Store) Clear(user string) (int,error) {
	s.mu.Lock();defer s.mu.Unlock();keep:=map[string]Record{}
	for id,r:=range s.records{if !matchesUser(r,user){keep[id]=r}}
	removed:=len(s.records)-len(keep);if removed==0{return 0,nil};return removed,s.rewrite(keep)
}
func (s *Store) Cleanup(days,limit int) (int,error) {
	s.mu.Lock();defer s.mu.Unlock();cutoff:=s.now().In(s.zone).AddDate(0,0,-days);rows:=make([]Record,0,len(s.records))
	for _,r:=range s.records{if days==0||!r.StartedAt.Before(cutoff){rows=append(rows,r)}}
	sortRecords(rows,"recent");if limit>0&&len(rows)>limit{rows=rows[:limit]};keep:=map[string]Record{};for _,r:=range rows{keep[r.ID]=r}
	n:=len(s.records)-len(keep);if n==0{return 0,nil};return n,s.rewrite(keep)
}
