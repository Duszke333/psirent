package peer

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"gitlab-stud.elka.pw.edu.pl/psi54/psirent/filedistrib/coms"
	"gitlab-stud.elka.pw.edu.pl/psi54/psirent/filedistrib/persistent"
	"gitlab-stud.elka.pw.edu.pl/psi54/psirent/internal/constants"
	errors2 "gitlab-stud.elka.pw.edu.pl/psi54/psirent/internal/errors"
)

var filename string
var filenameOnce sync.Once

func downloadFragment(peer string, fragNo int, totalFragments int, filehash string, results chan<- int, emptyFrags chan<- int, waitGroup *sync.WaitGroup) {
	defer waitGroup.Done()
	conn, err := net.Dial("tcp", peer)
	if err != nil {
		fmt.Println(err)
		results <- fragNo // mark as failed
		return
	}
	defer conn.Close()

	// Send fragment request
	if _, err = fmt.Fprintf(conn, "FRAG:%v:%v:%v\n", fragNo, totalFragments, filehash); err != nil {
		fmt.Println(err)
		results <- fragNo // mark as failed
		return
	}

	// Wait for response (and hopefully the fragment)
	reader := bufio.NewReader(conn)

	// Read filename
	fname, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println(err)
		results <- fragNo // mark as failed
		return
	}
	fname = strings.TrimSuffix(fname, "\n")
	// Set the filename (done only once)
	filenameOnce.Do(func() {
		filename = fname
	})

	// Try to read status
	status, err := reader.ReadString('\n')
	// Check if an error occurred
	if err != nil && err != io.EOF {
		fmt.Println(err)
		results <- fragNo // mark as failed
		return
	}
	msg := strings.TrimSuffix(status, "\n")
	if msg == coms.FragEmpty {
		fmt.Printf("Fragment %d is empty\n", fragNo)
		emptyFrags <- fragNo // mark as empty
		return
	} else if msg == coms.FragNotOk {
		fmt.Printf("Fragment %d is not ok\n", fragNo)
		results <- fragNo // mark as failed
		return
	} // else assume that status is just file content, will be written to file

	// Save fragment
	file, err := os.Create(fmt.Sprintf("%s.frag%d", filehash, fragNo))
	if err != nil {
		fmt.Println(err)
		results <- fragNo // mark as failed
		return
	}
	defer file.Close()

	// Read and save the fragment
	file.Write([]byte(status))
	buffer := make([]byte, constants.FILE_CHUNK)
	for {
		bytesRead, err := reader.Read(buffer)
		if err != nil {
			if err == io.EOF {
				break // End of file, stop reading
			}
			fmt.Println(err)
			results <- fragNo // mark as failed
			return            // Handle other errors
		}
		if bytesRead > 0 {
			if _, writeErr := file.Write(buffer[:bytesRead]); writeErr != nil {
				fmt.Println(writeErr)
				results <- fragNo // mark as failed
				return
			}
		}
	}
	fmt.Printf("Fragment %d saved\n", fragNo)
}

func Get(crw io.ReadWriter, filehash string, storage persistent.Storage) (err error) {
	// Reset filename
	filename = ""
	filenameOnce = sync.Once{}
	// Prevents size mismatch if retrying download
	var totalFragments int
	var totalFragmentsOnce sync.Once
	emptyFragSet := make(map[int]struct{})
	var failedFragments []int
	for {
		// Send
		if _, err = fmt.Fprintf(crw, "GET:%v\n", filehash); err != nil {
			return
		}
		// Wait
		scanner := bufio.NewScanner(crw)
		if !scanner.Scan() {
			err = errors2.ErrLostConnection
			return
		}

		response := scanner.Text()
		if response == coms.GetNoPeer {
			err = errors2.ErrGetNoPeerOnline
			return
		} else if response == coms.GetNotOK {
			err = errors2.ErrGetFileNotShared
			return
		}
		// Get the list of peers
		peerList := strings.Split(response, coms.LsSeparator)
		fmt.Printf("Peers: %v\n", peerList) // Temporary log
		totalFragmentsOnce.Do(func() {
			totalFragments = len(peerList)
		})
		// Get the fragments
		results := make(chan int, totalFragments)
		emptyFrags := make(chan int, totalFragments)
		var wg sync.WaitGroup

		// Populate the first run
		if failedFragments == nil {
			failedFragments = make([]int, totalFragments)
			for i := 0; i < totalFragments; i++ {
				failedFragments[i] = i + 1
			}
		}

		// Download fragments concurrently
		for i, fragNo := range failedFragments {
			wg.Add(1)
			peer := peerList[i%len(peerList)] // Safety measure if the list of peers length changes
			go downloadFragment(peer, fragNo, totalFragments, filehash, results, emptyFrags, &wg)
		}

		// Wait for all downloads to complete
		wg.Wait()
		close(results)
		close(emptyFrags)

		// Save empty fragment numbers
		for frag := range emptyFrags {
			emptyFragSet[frag] = struct{}{}
		}

		// Check for failed fragments
		failedFragments = nil
		for frag := range results {
			failedFragments = append(failedFragments, frag)
		}
		if len(failedFragments) == 0 {
			break // All fragments downloaded successfully
		}

		// Retry failed fragments
		fmt.Printf("Retrying failed fragments: %v\n", failedFragments)
	}

	// Check if the result folder exists and create it if not
	if _, err = os.Stat("received"); os.IsNotExist(err) {
		err = os.Mkdir("received", 0755)
		if err != nil {
			return err
		}
	}

	// Reassemble the file
	fmt.Printf("Reassembling the file %v...\n", filename)
	// Check if filename was set
	if filename == "" {
		err = fmt.Errorf("filename was not set by any peer")
		return
	}
	filename = "received/" + filename
	file, err := os.Create(filename)
	if err != nil {
		fmt.Println(err)
		return err
	}
	defer file.Close()

	for i := 1; i <= totalFragments; i++ {
		// Check if fragment is marked as empty
		if _, ok := emptyFragSet[i]; ok {
			fmt.Printf("Skipping empty fragment %d\n", i)
			continue
		}
		// Open fragment
		fileFragment, err := os.Open(fmt.Sprintf("%s.frag%d", filehash, i))
		if err != nil {
			return err
		}
		// Append fragment to file
		_, err = io.Copy(file, fileFragment)
		if err != nil {
			return err
		}
		fileFragment.Close()
		// Remove fragment
		err = os.Remove(fmt.Sprintf("%s.frag%d", filehash, i))
		if err != nil {
			return err
		}
	}
	fmt.Println("File reassembled!")
	fmt.Println("Sharing the file...")
	// Share the file
	err = HandleShare(crw, filename, storage)
	if err != nil {
		return err
	}
	return
}
