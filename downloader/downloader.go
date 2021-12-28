package main

import (
	"bvUtils/logger"
	"bvUtils/network"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	piece_state_start = iota
	piece_state_doing
	piece_state_done
)

type Piece struct {
	Start int
	State int
}

type PieceRet struct {
	Index int
}

type DownloadRecord struct {
	TotalSize  int
	BlockSize  int
	DonePieces []Piece
}

func (record *DownloadRecord) AddPiece(piece Piece) int {
	record.DonePieces = append(record.DonePieces, piece)
	sort.Slice(record.DonePieces, func(i, j int) bool {
		return record.DonePieces[i].Start < record.DonePieces[j].Start
	})

	for k, v := range record.DonePieces {
		if v.Start == piece.Start {
			return k
		}
	}

	return -1
}

func (record *DownloadRecord) Reset() {
	for k, v := range record.DonePieces {
		if v.State != piece_state_done {
			record.DonePieces[k].State = piece_state_start
		}
	}
}

type ProgressBar struct {
	done int
}

func (bar *ProgressBar) Write(p []byte) (int, error) {
	num := len(p)
	bar.done += num
	return num, nil
}

type HttpDownloader struct {
	url           string
	proxys        []string
	fileName      string
	contentLength int
	workChan      chan int
	doneChan      chan PieceRet
	startTime     int64
	startSize     int
	progressBars  []ProgressBar
	finish        int
}

func NewDownloader(urlStr, proxy, fileName string) *HttpDownloader {
	if fileName == "" {
		u, err := url.Parse(urlStr)
		if err != nil {
			logger.Error("url parse error:", err)
			return nil
		}

		arr := strings.Split(u.Path, "/")
		fileName = arr[len(arr)-1]
	}

	logger.Debug("file name:", fileName)

	proxyArr := strings.Split(proxy, "|")

	client, err := network.CreateHttpClient(proxyArr[0], true)
	if err != nil {
		logger.Error("create http client error:", err)
		return nil
	}

	rsp, err := client.Head(urlStr)
	if err != nil {
		logger.Error("client head error:", err)
		return nil
	}

	acceptRanges := rsp.Header.Get("Accept-Ranges")
	if acceptRanges != "bytes" {
		logger.Error("do not support download piece")
		return nil
	}

	logger.Printf(logger.LEVEL_INFO, "file size: %d MB (%d bytes)", rsp.ContentLength/(1024*1024), rsp.ContentLength)

	downloader := new(HttpDownloader)
	downloader.url = urlStr
	downloader.proxys = proxyArr
	downloader.contentLength = int(rsp.ContentLength)
	downloader.fileName = fileName
	downloader.workChan = make(chan int)
	downloader.doneChan = make(chan PieceRet)

	return downloader
}

func (hd *HttpDownloader) Download(record *DownloadRecord, recordFile string, numThreads int) {
	if record.TotalSize != 0 && record.TotalSize != hd.contentLength {
		logger.Error("file size error, new size:", hd.contentLength, "old size:", record.TotalSize)
		return
	}

	record.TotalSize = hd.contentLength
	hd.progressBars = make([]ProgressBar, numThreads)

	file, err := os.Open(hd.fileName)
	if err != nil {
		file, err = os.Create(hd.fileName)
		if err != nil {
			logger.Error("create file error:", err)
			return
		}
	}

	file.Close()

	ok := false
	for i := 0; i < 10; i++ {
		if hd.startDownload(record, recordFile, numThreads) {
			ok = true
			break
		}
	}

	if !ok {
		logger.Error("Download Failed!")
	}
}

func (hd *HttpDownloader) startDownload(record *DownloadRecord, recordFile string, numThreads int) bool {
	for k, v := range record.DonePieces {
		if v.State != piece_state_done {
			record.DonePieces[k].State = piece_state_start
		}
	}

	for i := 0; i < numThreads; i++ {
		hd.progressBars[i].done = 0
		go hd.startThread(record, i)
	}

	hd.startTime = time.Now().Unix()
	hd.startSize = hd.getDoneSize(record)

	endNum := 0
	hd.finish = 0

	go hd.showProgress()

	for {
		ret := <-hd.doneChan
		if ret.Index >= 0 {
			record.DonePieces[ret.Index].State = piece_state_done
			hd.saveProgress(record, recordFile)
		} else if ret.Index == -2 {
			endNum++
			if endNum == numThreads {
				break
			} else {
				continue
			}
		}

		index := hd.findPiece(record)
		if index >= 0 {
			record.DonePieces[index].State = piece_state_doing
		}

		hd.workChan <- index

		time.Sleep(1 * time.Second)
	}

	complete := true
	for _, v := range record.DonePieces {
		if v.State != piece_state_done {
			complete = false
			break
		}
	}

	if complete {
		hd.finish = 1
	} else {
		hd.finish = 2
	}

	<-hd.doneChan

	return complete
}

func (hd *HttpDownloader) showProgress() {
	var oldDone, remainSec, remainMin int
	doneSize := 0
	startTime := time.Now()
	progressLen := 0

	for hd.finish == 0 {
		time.Sleep(1 * time.Second)

		oldDone = doneSize
		doneSize = 0
		for _, v := range hd.progressBars {
			doneSize += v.done
		}

		speed := (doneSize - oldDone)
		avgSpeed := doneSize / (int(time.Since(startTime).Seconds()))
		donePercent := (doneSize + hd.startSize) * 100 / hd.contentLength
		remainSize := hd.contentLength - hd.startSize - doneSize

		if speed > 0 {
			remainSec = remainSize / speed
			remainMin = remainSec / 60
			remainSec = remainSec % 60
		}

		str := fmt.Sprintf("\rprogress => %d%% (%d/%d), current speed: %d KB/s, average speed: %d KB/s, remain: %d min %d sec",
			donePercent, doneSize+hd.startSize, hd.contentLength, speed/1024, avgSpeed/1024, remainMin, remainSec)

		if progressLen > len(str) {
			slice := make([]byte, progressLen)
			slice[0] = '\r'
			for k := range slice[1:] {
				slice[k+1] = ' '
			}

			fmt.Print(string(slice))
		}

		fmt.Print(str)
		progressLen = len(str)
	}

	fmt.Println("")

	if hd.finish == 1 {
		logger.Info("Download OK!")
	} else {
		logger.Warning("Download NOT completed!, restart download pieces")
	}

	hd.doneChan <- PieceRet{}
}

func (hd *HttpDownloader) startThread(record *DownloadRecord, threadIndex int) {
	ret := PieceRet{Index: -1}
	hd.doneChan <- ret

	for {
		index := <-hd.workChan
		if index < 0 {
			break
		}

		size := hd.downloadPiece(&record.DonePieces[index], record.BlockSize, threadIndex)
		if size > 0 {
			ret.Index = index
		} else {
			ret.Index = -1
		}

		hd.doneChan <- ret
	}

	ret.Index = -2
	hd.doneChan <- ret
}

func (hd *HttpDownloader) findPiece(record *DownloadRecord) int {
	if len(record.DonePieces) == 0 {
		piece := Piece{Start: 0}
		record.DonePieces = []Piece{piece}
		return 0
	}

	for i := 0; i < hd.contentLength; i += record.BlockSize {
		blockDone := false
		for k, v := range record.DonePieces {
			if v.Start == i {
				if v.State == piece_state_start {
					return k
				} else {
					blockDone = true
					break
				}
			}
		}

		if blockDone {
			continue
		}

		piece := Piece{Start: i}
		return record.AddPiece(piece)
	}

	return -1
}

func (hd *HttpDownloader) downloadPiece(piece *Piece, blockSize, threadIndex int) int {
	proxy := hd.proxys[threadIndex%len(hd.proxys)]
	client, err := network.CreateHttpClient(proxy, true)
	if err != nil {
		logger.Error("create http client error:", err)
		return 0
	}

	req, err := http.NewRequest("GET", hd.url, nil)
	if err != nil {
		logger.Error("create http get request error:", err)
		return 0
	}

	end := piece.Start + blockSize - 1
	if end >= hd.contentLength {
		end = hd.contentLength - 1
	}

	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", piece.Start, end))

	rsp, err := client.Do(req)
	if err != nil {
		logger.Error("client get error:", err)
		return 0
	}

	var buf bytes.Buffer
	_, err = io.Copy(io.MultiWriter(&buf, &hd.progressBars[threadIndex]), rsp.Body)
	if err != nil {
		logger.Error("read http rsp body error:", err)
		return 0
	}

	if !hd.saveFile(piece.Start, &buf) {
		return 0
	}

	return end + 1 - piece.Start
}

func (hd *HttpDownloader) saveFile(offset int, buf *bytes.Buffer) bool {
	file, err := os.OpenFile(hd.fileName, os.O_WRONLY, 0660)
	if err != nil {
		logger.Error("open file error:", err)
		return false
	}
	defer file.Close()

	_, err = file.Seek(int64(offset), 0)
	if err != nil {
		logger.Error("file seek error:", err)
		return false
	}

	_, err = file.Write(buf.Bytes())
	if err != nil {
		logger.Error("file write error:", err)
		return false
	}

	return true
}

func (hd *HttpDownloader) getDoneSize(record *DownloadRecord) int {
	if len(record.DonePieces) == 0 {
		return 0
	}

	doneSize := 0
	if len(record.DonePieces)*record.BlockSize > record.TotalSize {
		if record.DonePieces[len(record.DonePieces)-1].State == piece_state_done {
			doneSize = record.TotalSize - (len(record.DonePieces)-1)*record.BlockSize
		}

		for _, v := range record.DonePieces[:len(record.DonePieces)-1] {
			if v.State == piece_state_done {
				doneSize += record.BlockSize
			}
		}
	} else {
		for _, v := range record.DonePieces {
			if v.State == piece_state_done {
				doneSize += record.BlockSize
			}
		}
	}

	return doneSize
}

func (hd *HttpDownloader) saveProgress(record *DownloadRecord, recordFile string) {
	slice, err := json.MarshalIndent(record, "", "    ")
	if err != nil {
		logger.Error("json encode record error:", err)
		return
	}

	err = ioutil.WriteFile(recordFile, slice, 0666)
	if err != nil {
		logger.Error("save record file error:", err)
		return
	}
}
