package wtfd

import (
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"

	"github.com/gomarkdown/markdown"
	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	"golang.org/x/crypto/bcrypt"
)

const (
	DefaultPort = int64(80)
)

var (
	// key must be 16, 24 or 32 bytes long (AES-128, AES-192 or AES-256)
	key                 = []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	store               = sessions.NewFilesystemStore("", key) // generates filesystem store at os.tempdir
	ErrUserExisting     = errors.New("User with this name exists")
	ErrWrongPassword    = errors.New("Wrong Password")
	ErrUserNotExisting  = errors.New("User with this name does not exist")
	sshHost             = "localhost:2222"
	challs              = Challenges{}
	challcats           = ChallengeCategory{}
	mainpagetemplate    = template.New("")
	leaderboardtemplate = template.New("")
)

type Challenges []Challenge
type ChallengeCategories []ChallengeCategory

type JsonFile struct {
	Categories []ChallengeCategoryJson `json:"categories"`
	Challenges []ChallengeJson         `json:"challenges"`
}

type ChallengeCategory struct {
	Title      string `json:"title"`
	Challenges Challenges
}

type ChallengeCategoryJson struct {
	Title      string   `json:"title"`
	Challenges []string `json:"challs"`
}

type Challenge struct {
	Name        string `json:"name"`
	Description string `json:"desc"`
	Flag        string `json:"flag"`
	Points      int    `json:"points"`
	Uri         string `json:"uri"`
	DepCount    int
	Solution    string `json:"solution"`
	DepIds      []string
	Deps        []Challenge
	HasUri      bool // This emerges from Uri != ""
}

type ChallengeJson struct {
	Name        string   `json:"name"`
	Description string   `json:"desc"`
	Solution    string   `json:"solution"`
	Flag        string   `json:"flag"`
	Points      int      `json:"points"`
	Uri         string   `json:"uri"`
	Deps        []string `json:"deps"`
	HasUri      bool     // This emerges from Uri != ""
}

type User struct {
	Name      string
	Hash      []byte
	Completed []Challenge
}

type MainPageData struct {
	PageTitle              string
	Challenges             []Challenge
	SelectedChallengeId    string
	HasSelectedChallengeId bool
	User                   User
	IsUser                 bool
	Points                 int
}

/**
 * Fill host into each challenge's Uri field and set HasUri
 */
func (c Challenges) FillChallengeUri(host string) {
	for i, _ := range c {
		if c[i].Uri != "" {
			c[i].HasUri = true
			c[i].Uri = fmt.Sprintf(c[i].Uri, host)
		} else {
			c[i].HasUri = false
		}
	}
}

func (c Challenges) Find(id string) (Challenge, error) {
	for _, v := range c {
		if v.Name == id {
			return v, nil
		}
	}
	return Challenge{}, fmt.Errorf("No challenge with this id")
}

func (c Challenge) AllDepsCompleted(u User) bool {
	for _, ch := range c.Deps {
		a := false
		for _, uch := range u.Completed {
			if uch.Name == ch.Name {
				a = true
			}
		}
		if a == false {
			return false
		}
	}
	return true
}

func Contains(username string) bool {
	_, err := ormLoadUser(username)
	return err == nil
}

func (u User) HasSolvedChallenge(chall Challenge) bool {
	for _, c := range u.Completed {
		if c.Name == chall.Name {
			return true
		}
	}
	return false
}

func (u User) CalculatePoints() int {
	points := 0

	for _, c := range u.Completed {
		points += c.Points
	}

	return points
}

func Get(username string) (User, error) {
	user, err := ormLoadUser(username)
	if err != nil {
		return User{}, ErrUserNotExisting
	}
	return user, err

}

func resolveDeps(a []string) []Challenge {
	toReturn := []Challenge{}
	for _, b := range a {
		for _, c := range challs {
			if c.Name == b {
				toReturn = append(toReturn, c)
			}
		}
	}
	return toReturn

}

func countDeps(chall Challenge) int {
	max := 1
	if len(chall.Deps) == 0 {
		return 0

	}
	for _, a := range chall.Deps {
		depcount := countDeps(a)
		if depcount+1 > max {
			max = depcount + 1
		}
	}
	//return len(chall.DepIds) + max
	return max

}

func countAllDeps() {
	for i, _ := range challs {
		challs[i].DepCount = countDeps(challs[i])
	}
}
func reverseResolveAllDepIds() {
	for i, _ := range challs {
		for j, _ := range challs {
			if i != j {
				for _, d := range challs[j].Deps {
					if d.Name == challs[i].Name {
						fmt.Printf("%s hat %s als revers dep\n", challs[i].Name, challs[j].Name)
						challs[i].DepIds = append(challs[i].DepIds, challs[j].Name)
						break
					}
				}
			}
		}
	}
}

func resolveChalls(challcat []ChallengeJson) {
	i := 0
	idsInChalls := []string{}
	for len(challcat) != 0 {
		//          fmt.Printf("challs: %v, challcat: %v\n",challs,challcat)
		this := challcat[i]
		if bContainsAllOfA(this.Deps, idsInChalls) {
			idsInChalls = append(idsInChalls, this.Name)
			challs = append(challs, Challenge{Name: this.Name, Description: this.Description, Flag: this.Flag, Uri: this.Uri, Points: this.Points, Deps: resolveDeps(this.Deps), Solution: this.Solution})
			challcat[i] = challcat[len(challcat)-1]
			challcat = challcat[:len(challcat)-1]
			i = 0
		} else {
			i++
		}

	}
	countAllDeps()
	reverseResolveAllDepIds()
}

func Login(username, passwd string) (User, error) {
	user, err := Get(username)
	if err != nil {
		return User{}, err
	}
	if pwdRight := user.ComparePassword(passwd); !pwdRight {
		return User{}, ErrWrongPassword
	}
	fmt.Printf("User login: %s\n", username)
	return user, nil

}

func (u *User) ComparePassword(password string) bool {
	return bcrypt.CompareHashAndPassword(u.Hash, []byte(password)) == nil
}

func NewUser(name, password string) (User, error) {
	if Contains(name) {
		return User{}, ErrUserExisting
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	if err != nil {
		return User{}, err
	}

	fmt.Printf("New User added: %s\n", name)
	return User{Name: name, Hash: hash}, nil

}

func mainpage(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hasChall := vars["chall"] != ""
	session, _ := store.Get(r, "auth")
	val := session.Values["User"]
	user := &User{}
	newuser, ok := val.(*User)
	if ok {
		user = newuser
	}
	data := MainPageData{
		PageTitle:              "foss-ag O-Phasen CTF",
		Challenges:             challs,
		HasSelectedChallengeId: hasChall,
		SelectedChallengeId:    vars["chall"],
		User:                   *user,
		IsUser:                 ok,
	}
	err := mainpagetemplate.Execute(w, data)
	if err != nil {
		fmt.Printf("Template error: %v\n", err)

	}

}

func login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "Invalid Request")

	} else {
		if err := r.ParseForm(); err != nil {
			fmt.Fprintf(w, "ParseForm() err: %v", err)
			return
		}
		session, _ := store.Get(r, "auth")
		if _, ok := session.Values["User"].(*User); ok {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "Already logged in")
		} else {
			u, err := Login(r.Form.Get("username"), r.Form.Get("password"))
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprintf(w, "Server Error: %v", err)
			} else {
				session.Values["User"] = u
				session.Save(r, w)
				http.Redirect(w, r, "/", http.StatusFound)

			}

		}

	}

}

func submitFlag(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "Invalid Request")

	} else {
		if err := r.ParseForm(); err != nil {
			fmt.Fprintf(w, "ParseForm() err: %v", err)
			return
		}
		session, _ := store.Get(r, "auth")

		user, ok := session.Values["User"].(*User)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "Server Error: %v", "Not logged in")
			return
		}
		completedChallenge, err := challs.Find(r.Form.Get("challenge"))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "Server Error: %v", err)
			return
		}
		if r.Form.Get("flag") == completedChallenge.Flag {
			user.Completed = append(user.Completed, completedChallenge)
			if err = ormSolvedChallenge(*user, completedChallenge); err != nil {
				fmt.Errorf("ORM Error: %s\n", err.Error())
			}
			fmt.Fprintf(w, "correct")

		} else {
			fmt.Fprintf(w, "not correct")
		}
		session.Save(r, w)

	}

}

func register(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "Invalid Request")

	} else {
		if err := r.ParseForm(); err != nil {
			fmt.Fprintf(w, "ParseForm() err: %v", err)
			return
		}
		session, _ := store.Get(r, "auth")
		if _, ok := session.Values["User"].(*User); ok {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "Already logged in")
		} else {

			if len(r.Form.Get("username")) < 5 {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "Username must be at least 5 characters")

			} else {
				u, err := NewUser(r.Form.Get("username"), r.Form.Get("password"))
				if err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprintf(w, "Server Error: %v", err)
				} else {
					session.Values["User"] = u
					ormNewUser(u)
					session.Save(r, w)
					http.Redirect(w, r, "/", http.StatusFound)

				}

			}
		}

	}

}

func logout(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "auth")
	session.Values["User"] = nil
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)

}

func solutionview(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	chall, err := challs.Find(vars["chall"])
	if err != nil {
		fmt.Fprintf(w, "ServerError: Challenge with is %s not found")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	session, _ := store.Get(r, "auth")
	u, ok := session.Values["User"].(*User)
	if !ok {
		fmt.Fprintf(w, "ServerError: not logged in")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !u.HasSolvedChallenge(chall) {
		fmt.Fprintf(w, "did you just try to pull a sneaky on me?")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	md := markdown.ToHTML([]byte(chall.Solution), nil, nil)
	fmt.Fprintf(w, "%s", md)

}

func detailview(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	chall, err := challs.Find(vars["chall"])
	if err != nil {
		fmt.Fprintf(w, "ServerError: Challenge with is %s not found")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	md := markdown.ToHTML([]byte(chall.Description), nil, nil)
	fmt.Fprintf(w, "%s", md)

}

func favicon(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "html/static/favicon.ico")
}

func Server() error {
	gob.Register(&User{})

	// Loading challs file
	challsFile, err := os.Open("challs.json")
	if err != nil {
		return err
	}
	defer challsFile.Close()
	var challsStructure JsonFile
	challsFileBytes, _ := ioutil.ReadAll(challsFile)
	if err := json.Unmarshal(challsFileBytes, &challsStructure); err != nil {
		return err
	}
	resolveChalls(challsStructure.Challenges)

	// Load database
	ormStart("./dblog")

	// Fill in sshHost
	challs.FillChallengeUri(sshHost)

	// Parse Templates
	mainpagetemplate, err = template.ParseFiles("html/index.html", "html/footer.html", "html/header.html")
	if err != nil {
		fmt.Println(err)
	}
	// Http sturf
	r := mux.NewRouter()
	r.HandleFunc("/", mainpage)
	r.HandleFunc("/favicon.ico", favicon)
	r.HandleFunc("/login", login)
	r.HandleFunc("/logout", logout)
	r.HandleFunc("/register", register)
	r.HandleFunc("/submitflag", submitFlag)
	r.HandleFunc("/{chall}", mainpage)
	r.HandleFunc("/detailview/{chall}", detailview)
	r.HandleFunc("/solutionview/{chall}", solutionview)
	// static
	r.PathPrefix("/static").Handler(
		http.StripPrefix("/static/", http.FileServer(http.Dir("html/static"))))

	Port := DefaultPort
	if portenv := os.Getenv("WTFD_PORT"); portenv != "" {
		Port, _ = strconv.ParseInt(portenv, 10, 64)
	}
	fmt.Printf("WTFD Server Starting at port %d\n", Port)
	return http.ListenAndServe(fmt.Sprintf(":%d", Port), r)
}

func bContainsA(a string, b []string) bool {
	for _, c := range b {
		if a == c {
			return true
		}

	}
	return false

}

func bContainsAllOfA(a, b []string) bool {
	for _, c := range a {
		if !bContainsA(c, b) {
			return false
		}
	}
	return true
}
