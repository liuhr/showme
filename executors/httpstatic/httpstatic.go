package httpstatic

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jroimartin/gocui"
	"github.com/lflxp/showme/utils"
)

var (
	path      string
	port      string
	closeChan chan os.Signal
	uri       string
	initnum   int
)

func init() {
	initnum = 0
	path = GetCurrentDirectory()
	port = "9090"
	closeChan = make(chan os.Signal)
}

func GetCurrentDirectory() string {
	dir, err := filepath.Abs(filepath.Dir(os.Args[0])) //返回绝对路径  filepath.Dir(os.Args[0])去除最后一个元素的路径
	if err != nil {
		log.Fatal(err)
	}
	return strings.Replace(dir, "\\", "/", -1) //将\替换成/
}

func server() {
	if initnum == 0 {
		http.Handle("/", http.FileServer(http.Dir(path)))
		initnum++
	}

	// http.ListenAndServe(fmt.Sprintf(":%s", port), nil)
	server := &http.Server{
		Addr:    fmt.Sprintf(":%s", port),
		Handler: http.DefaultServeMux,
	}
	signal.Notify(closeChan,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGHUP,
		os.Interrupt,
		os.Kill,
	)
	go func() {
		<-closeChan
		server.Close()
		// server.RegisterOnShutdown(func() { return })
		fmt.Println(utils.Colorize("quit", "red", "", false, false))
	}()
	go server.ListenAndServe()
}

func HttpStaticServe(in string) {
	// httpstatic -port 9090 -path ./
	tmp := strings.Split(in, " ")
	for n, x := range tmp {
		if x == "-port" {
			port = tmp[n+1]
		}
	}

	g, err := gocui.NewGui(gocui.OutputNormal)
	if err != nil {
		log.Panicln(err)
	}
	defer g.Close()

	g.Highlight = true
	g.Cursor = true
	g.SelFgColor = gocui.ColorGreen
	g.SetManagerFunc(dlayout)

	// if err := g.SetKeybinding("", gocui.KeyCtrlC, gocui.ModNone, dquit); err != nil {
	// 	log.Panicln(err)
	// }

	if err := keybindings(g); err != nil {
		log.Panicln(err)
	}

	if err := g.MainLoop(); err != nil && err != gocui.ErrQuit {
		log.Panicln(err)
	}
}

func keybindings(g *gocui.Gui) error {
	// 清空side缓存
	// if err := g.SetKeybinding("help", gocui.KeyTab, gocui.ModNone, nextView); err != nil {
	// 	return err
	// }
	if err := g.SetKeybinding("", gocui.KeyCtrlC, gocui.ModNone, dquit); err != nil {
		return err
	}
	return nil
}

func dquit(g *gocui.Gui, v *gocui.View) error {
	closeChan <- syscall.SIGINT
	return gocui.ErrQuit
}

func setCurrentViewOnTop(g *gocui.Gui, name string) (*gocui.View, error) {
	if _, err := g.SetCurrentView(name); err != nil {
		return nil, err
	}
	return g.SetViewOnTop(name)
}

func dlayout(g *gocui.Gui) error {
	maxX, maxY := g.Size()
	if v, err := g.SetView("top", maxX/2-60, maxY/2, maxX/2+60, maxY/2+3); err != nil {
		if err != gocui.ErrUnknownView {
			return err
		}
		v.Title = "静态服务器地址"
		v.Wrap = true
		// v.Highlight = true
		// v.Autoscroll = true
		v.SelBgColor = gocui.ColorGreen
		v.SelFgColor = gocui.ColorBlack
		// v.Editable = true
		// fmt.Fprintf(v, time.Now().Format("2006-01-02 15:04:05"))
		// uri = fmt.Sprintf("/a%s", time.Now().Format("150405"))
		fmt.Fprintln(v, fmt.Sprintf("URL => 0.0.0.0:%s <= \nPATH: => %s <=", port, path))
		go server()

		if _, err = setCurrentViewOnTop(g, "top"); err != nil {
			return err
		}
	}
	return nil
}
