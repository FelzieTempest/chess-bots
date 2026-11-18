package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

//go:embed static
var content embed.FS

// ---------- online matchmaking ----------

type Player struct {
	ID, Name string
	TC       int
	ch       chan []byte
	g        *Game
	color    string
}

type Game struct {
	w, b   *Player
	over   bool
	drawBy string
}

type Req struct {
	PID    string `json:"pid"`
	Name   string `json:"name"`
	TC     int    `json:"tc"`
	F      int    `json:"f"`
	T      int    `json:"t"`
	Pr     string `json:"pr"`
	Clock  int    `json:"clock"`
	Result string `json:"result"`
	Reason string `json:"reason"`
	Text   string `json:"text"`
}

var (
	mu      sync.Mutex
	players = map[string]*Player{}
	queue   = map[int]*Player{}
	ratings = map[string]int{}
)

func send(p *Player, v any) {
	b, _ := json.Marshal(v)
	select {
	case p.ch <- b:
	default:
	}
}

func rating(n string) int {
	if r, ok := ratings[n]; ok {
		return r
	}
	ratings[n] = 1200
	return 1200
}

func (g *Game) opp(p *Player) *Player {
	if g.w == p {
		return g.b
	}
	return g.w
}

// end must be called with mu held
func end(g *Game, result, reason string) {
	if g.over {
		return
	}
	g.over = true
	rw, rb := rating(g.w.Name), rating(g.b.Name)
	ew := 1 / (1 + math.Pow(10, float64(rb-rw)/400))
	sw := 0.5
	if result == "w" {
		sw = 1
	} else if result == "b" {
		sw = 0
	}
	d := int(math.Round(32 * (sw - ew)))
	if g.w.Name == g.b.Name {
		d = 0
	}
	ratings[g.w.Name] = rw + d
	ratings[g.b.Name] = rb - d
	send(g.w, map[string]any{"type": "end", "result": result, "reason": reason, "delta": d, "rating": rw + d})
	send(g.b, map[string]any{"type": "end", "result": result, "reason": reason, "delta": -d, "rating": rb - d})
}

func api(h func(*Player, Req, http.ResponseWriter)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			return
		}
		var q Req
		json.NewDecoder(r.Body).Decode(&q)
		mu.Lock()
		defer mu.Unlock()
		p := players[q.PID]
		h(p, q, w)
	}
}

func handleQueue(_ *Player, q Req, w http.ResponseWriter) {
	if q.Name == "" || len(q.Name) > 20 {
		q.Name = fmt.Sprintf("Guest%d", rand.Intn(9999))
	}
	if q.TC != 60 && q.TC != 180 && q.TC != 300 && q.TC != 600 {
		q.TC = 300
	}
	p := &Player{ID: fmt.Sprintf("%x", rand.Int63()), Name: q.Name, TC: q.TC, ch: make(chan []byte, 64)}
	players[p.ID] = p
	if o := queue[q.TC]; o != nil {
		delete(queue, q.TC)
		g := &Game{w: o, b: p}
		if rand.Intn(2) == 0 {
			g.w, g.b = p, o
		}
		g.w.g, g.b.g, g.w.color, g.b.color = g, g, "w", "b"
		for _, x := range []*Player{g.w, g.b} {
			op := g.opp(x)
			send(x, map[string]any{"type": "start", "color": x.color, "tc": q.TC, "opp": op.Name, "oppRating": rating(op.Name), "rating": rating(x.Name)})
		}
	} else {
		queue[q.TC] = p
	}
	json.NewEncoder(w).Encode(map[string]any{"pid": p.ID, "rating": rating(p.Name), "name": p.Name})
}

func handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	mu.Lock()
	p := players[r.URL.Query().Get("pid")]
	mu.Unlock()
	fl, ok := w.(http.Flusher)
	if p == nil || !ok {
		http.Error(w, "unknown player", 404)
		return
	}
	fmt.Fprint(w, ": hi\n\n")
	fl.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case m := <-p.ch:
			fmt.Fprintf(w, "data: %s\n\n", m)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case <-r.Context().Done():
			mu.Lock()
			if queue[p.TC] == p {
				delete(queue, p.TC)
			}
			if p.g != nil && !p.g.over {
				end(p.g, p.g.opp(p).color, "abandonment")
			}
			delete(players, p.ID)
			mu.Unlock()
			return
		}
	}
}

func inGame(p *Player) bool { return p != nil && p.g != nil && !p.g.over }

func main() {
	rand.Seed(time.Now().UnixNano())
	sub, _ := fs.Sub(content, "static")
	http.Handle("/", http.FileServer(http.FS(sub)))
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	http.HandleFunc("/api/events", handleEvents)
	http.HandleFunc("/api/queue", api(handleQueue))
	http.HandleFunc("/api/cancel", api(func(p *Player, _ Req, _ http.ResponseWriter) {
		if p != nil && queue[p.TC] == p {
			delete(queue, p.TC)
		}
	}))
	http.HandleFunc("/api/move", api(func(p *Player, q Req, _ http.ResponseWriter) {
		if inGame(p) {
			p.g.drawBy = ""
			send(p.g.opp(p), map[string]any{"type": "move", "f": q.F, "t": q.T, "pr": q.Pr, "clock": q.Clock})
		}
	}))
	http.HandleFunc("/api/gameover", api(func(p *Player, q Req, _ http.ResponseWriter) {
		if inGame(p) && (q.Result == "w" || q.Result == "b" || q.Result == "d") {
			end(p.g, q.Result, q.Reason)
		}
	}))
	http.HandleFunc("/api/resign", api(func(p *Player, _ Req, _ http.ResponseWriter) {
		if inGame(p) {
			end(p.g, p.g.opp(p).color, "resignation")
		}
	}))
	http.HandleFunc("/api/draw", api(func(p *Player, _ Req, _ http.ResponseWriter) {
		if !inGame(p) {
			return
		}
		op := p.g.opp(p)
		if p.g.drawBy == op.color {
			end(p.g, "d", "agreement")
		} else {
			p.g.drawBy = p.color
			send(op, map[string]any{"type": "drawoffer"})
		}
	}))
	http.HandleFunc("/api/chat", api(func(p *Player, q Req, _ http.ResponseWriter) {
		if p != nil && p.g != nil && len(q.Text) > 0 {
			if len(q.Text) > 200 {
				q.Text = q.Text[:200]
			}
			send(p.g.opp(p), map[string]any{"type": "chat", "from": p.Name, "text": q.Text})
		}
	}))

	port := os.Getenv("PORT") // Render sets PORT automatically
	if port != "" {
		log.Printf("Chess server running on :%s", port)
		log.Fatal(http.ListenAndServe(":"+port, nil))
	}

	// Desktop mode: free local port, open the game in its own app window
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	go openWindow(url)
	log.Fatal(http.Serve(ln, nil))
}

func openWindow(url string) {
	if runtime.GOOS == "windows" {
		if exec.Command("cmd", "/c", "start", "", "msedge", "--app="+url, "--window-size=1050,800").Run() == nil {
			return
		}
		exec.Command("cmd", "/c", "start", "", url).Run()
		return
	}
	exec.Command("xdg-open", url).Run()
}
