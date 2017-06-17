package core

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/PuerkitoBio/goquery"
	log "gopkg.in/clog.v1"
)

var (
	bufferSize = 1 << 16
	reqHeader  = map[string]string{
		"Pragma":          "no-cache",
		"Connection":      "keep-alive",
		"Cache-Control":   "no-cache",
		"Accept-Encoding": "gzip, deflate",
		"User-Agent":      "Chrome/57.0.2987.110",
	}
)

//
// Config ...
type Config struct {
	Destination string // destination to save pictures
	BaseURL     string // http://jandan.net
	StartPage   int32  // page to start from
	MaxPages    int32  // max page to crawl
}

type picture struct {
	Page int32
	URL  string
}

type janDan struct {
	Config
	scheme     string       // http or https
	pageNum    int32        // next page num
	totalCount int32        // total pictures count
	downCount  int32        // downloaded pictures
	errsCount  int32        // error count
	buffPool   *sync.Pool   // buffer for io
	chanPics   chan picture // channel to hold the picture links
	errLists   chan picture // used for save error download links
	finish     chan bool    // finish signal, stop by close channel
	mx         sync.Mutex
}

// NewJanDan ...
func NewJanDan(config Config) *janDan {
	jd := &janDan{
		Config:   config,
		pageNum:  config.StartPage,
		chanPics: make(chan picture, 20),
		errLists: make(chan picture, 20),
		finish:   make(chan bool, 1),
	}

	jd.buffPool = &sync.Pool{
		New: func() interface{} { return make([]byte, bufferSize) },
	}

	if fURL, err := url.Parse(config.BaseURL); err != nil {
		panic(err)
	} else {
		jd.scheme = fURL.Scheme
	}

	return jd
}

func (jd *janDan) Run() {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		jd.crawler()
		wg.Done()
	}()
	go func() {
		jd.downloader()
		wg.Done()
	}()

	wg.Wait()
	log.Info("Done.")
}

func (jd *janDan) Stop() {
	close(jd.finish)
}

func (jd *janDan) crawler() {
	log.Info("Page crawler start ...")
	for ok := true; ok; {
		nextPage := jd.nextPage()
		if nextPage == "" {
			log.Warn("Emtpy page url. Consider finished.")
			break
		}

		if err := jd.crawlPage(nextPage); err != nil {
			log.Warn("Page crawler error %+v.", err)
			continue
		}

		select {
		case ok = <-jd.finish:
		default:
		}
	}

	log.Info("Page Count: %d.", jd.pageNum-jd.StartPage)
	log.Info("Page crawler done. ")
	close(jd.chanPics)
}

func (jd *janDan) crawlPage(page string) error {
	if page == "" {
		return errors.New("empty page url")
	}

	log.Trace("Page %s", page)
	doc, err := goquery.NewDocument(page)
	if err != nil {
		log.Error(2, "Failed to get page %s, error %+v.", page, err)
		return err
	}

	//s, _ := doc.Find("ol.commentlist li p").Html()
	//log.Info("Page %d: %+v", jd.pageNum, s)

	doc.Find("ol.commentlist li p").Each(func(i int, p *goquery.Selection) {
		p.Find("a").Each(func(j int, a *goquery.Selection) {
			if href, exists := a.Attr("href"); exists {
				select {
				case jd.chanPics <- picture{jd.pageNum, href}:
					atomic.AddInt32(&jd.totalCount, 1)
				case <-jd.finish:
					break
				}
			}
		})
	})

	if doc.Find("a.next-comment-page").Length() == 0 {
		log.Warn("Last page. End crawl.")
		close(jd.finish)
	}

	atomic.AddInt32(&jd.pageNum, 1)
	return nil
}

func (jd *janDan) nextPage() string {
	// http://jandan.net/ooxx/page-111

	if jd.MaxPages != 0 && jd.pageNum-jd.StartPage >= jd.MaxPages {
		log.Warn("Up to the max pages %d.", jd.MaxPages)
		return ""
	}

	return fmt.Sprintf("%s/ooxx/page-%d", jd.BaseURL, jd.pageNum)
}

// downloader used to download pictures
func (jd *janDan) downloader() {
	var wg sync.WaitGroup
	wg.Add(2)
	log.Info("Downloader start ...")

	// single downloader for error list
	go func() {
		defer wg.Done()
		for pic := range jd.errLists {
			wg.Add(1)
			go func(pic picture) {
				defer wg.Done()
				jd.downloadFile(pic)
			}(pic)
		}
	}()

	go func() {
		defer wg.Done()
		for pic := range jd.chanPics {
			wg.Add(1)
			go func(pic picture) {
				defer wg.Done()
				jd.downloadFile(pic)
			}(pic)
		}
		close(jd.errLists)
	}()

	wg.Wait()
	log.Info("Picture, total %d, download %d, failed %d", jd.totalCount, jd.downCount, jd.errsCount)
}

func (jd *janDan) parseURL(addr string) (picURL, picName string) {
	if !strings.HasPrefix(addr, jd.scheme) {
		picURL = fmt.Sprintf("%s:%s", jd.scheme, addr)
	}

	if pos := strings.LastIndex(addr, "/"); pos == -1 {
		log.Warn("Invalid pictures address (%+v).", addr)
	} else {
		picName = addr[pos+1:]
	}
	return
}

func (jd *janDan) downloadFile(pic picture) error {
	defer func() {
		if err := recover(); err != nil {
			log.Error(2, "Recover from downloadFile panic: %+v.", err)
		}
	}()

	// get file name from url
	fullURL, fileName := jd.parseURL(pic.URL)
	if fileName == "" || fullURL == "" {
		return fmt.Errorf("invalid url (%+v)", pic.URL)
	}

	var (
		err      error
		resp     *http.Response
		fullPath string
	)

	for {
		if resp, err = http.Get(fullURL); err != nil {
			log.Error(0, "http.Get failed. error %+v.", err)
			if resp != nil {
				resp.Body.Close()
			}
			break
		}

		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Error(0, "http response status %d, %s", resp.StatusCode, resp.Status)
			break
		}

		log.Trace("Downloading %d, Size:%-8d, %+v", jd.downCount, resp.ContentLength, fullURL)
		if err = jd.saveContent(resp, jd.savePath(pic.Page, fileName)); err != nil {
			log.Error(0, "Failed to download %s, error %+v.", fullURL, err)
			break
		}

		atomic.AddInt32(&jd.downCount, 1)
		break
	}

	if err != nil {
		os.Remove(fullPath)
		atomic.AddInt32(&jd.errsCount, 1)
		if _, ok := <-jd.finish; ok {
			// if finished, will not retry any more
			jd.errLists <- pic
		}
	}

	return err
}

func (jd *janDan) savePath(pageID int32, filename string) string {
	subDir := filepath.Join(jd.Destination, fmt.Sprintf("%03d", pageID))
	if err := os.Mkdir(subDir, os.ModePerm); err != nil && !os.IsExist(err) {
		log.Error(0, "Failed to create folder %+v.", subDir)
		subDir = jd.Destination
	}

	return filepath.Join(subDir, filename)
}

func (jd *janDan) saveContent(resp *http.Response, file string) (err error) {
	var (
		fp     *os.File
		reader io.Reader
	)

	switch resp.Header.Get("Content-Encoding") {
	case "gzip":
		reader, _ = gzip.NewReader(resp.Body)
	default:
		reader = resp.Body
	}

	if fp, err = os.Create(file); err != nil {
		return
	}

	buff := jd.buffPool.Get().([]byte)
	defer jd.buffPool.Put(buff)

	if _, err = io.CopyBuffer(fp, reader, buff[:]); err != nil {
		fp.Close()
		os.Remove(file)
		return
	}

	return nil
}
