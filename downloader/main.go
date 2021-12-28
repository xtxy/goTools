package main

import (
	"bvUtils/file"
	"bvUtils/logger"
	"flag"
)

func main() {
	var urlStr, recordFileName, fileName, proxy string
	var numThreads, blockSize int

	logger.ParseLevel("stdout")

	flag.StringVar(&urlStr, "u", "", "url")
	flag.StringVar(&recordFileName, "r", "", "record file")
	flag.StringVar(&fileName, "f", "", "file name")
	flag.IntVar(&numThreads, "n", 1, "thread num")
	flag.IntVar(&blockSize, "b", 4, "block size, in MB")
	flag.StringVar(&proxy, "p", "", "proxy")

	flag.Parse()

	downloader := NewDownloader(urlStr, proxy, fileName)
	if downloader == nil {
		return
	}

	var record DownloadRecord
	if recordFileName != "" {
		err := file.ReadCfg(recordFileName, &record)
		if err != nil {
			logger.Error("load record file error:", err)
			return
		}
	} else {
		recordFileName = downloader.fileName + "_record.json"
		file.ReadCfg(recordFileName, &record)
	}

	if record.BlockSize == 0 {
		record.BlockSize = blockSize * 1024 * 1024
	}

	downloader.Download(&record, recordFileName, numThreads)
}
