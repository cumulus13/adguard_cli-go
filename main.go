package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	configget "github.com/cumulus13/go-config-get"
	"github.com/fatih/color"
	"github.com/spf13/pflag"
	"golang.org/x/net/idna"
)

// Terminal Color Definitions matching hex styles exactly
var (
	colorYellow = color.New(color.FgYellow, color.Bold).SprintfFunc()   // #FFFF00
	colorCyan   = color.New(color.FgCyan, color.Bold).SprintfFunc()     // #00FFFF
	colorPurple = color.New(color.FgHiMagenta).SprintfFunc()            // #AA55FF
	colorRed    = color.New(color.FgRed, color.Bold).SprintfFunc()      // #FF0000
	colorWhite  = color.New(color.FgWhite).SprintfFunc()                // white
	colorProg   = color.New(color.FgHiBlue, color.Bold, color.Italic).SprintfFunc() // #00AAFF
)

type RewriteEntry struct {
	Domain string `json:"domain"`
	Answer string `json:"answer"`
}

type AdguardHome struct {
	Host     string
	Port     int
	Username string
	Password string
	Scheme   string
	Client   *http.Client
}

func locateConfigFile() string {
	var candidates []string

	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "adguardhome.ini"))
	}

	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "adguardhome.ini"))
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	if cwd, err := os.Getwd(); err == nil {
		return filepath.Join(cwd, "adguardhome.ini")
	}
	return "adguardhome.ini"
}

func generateDefaultConfig(path string) error {
	content := `[auth]
host = 127.0.0.1
port = 88
username = licmon
password = Xxxnuxer13
scheme = http
`
	err := os.WriteFile(path, []byte(content), 0644)
	if err == nil {
		fmt.Printf("%s %s\n", colorYellow("Config created at:"), colorCyan(path))
	}
	return err
}

func getIniValue(filePath, section, key string) string {
	file, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	inSection := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSec := strings.ToLower(strings.Trim(line, "[]"))
			inSection = (currentSec == strings.ToLower(section))
			continue
		}
		if inSection {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				k := strings.TrimSpace(parts[0])
				v := strings.TrimSpace(parts[1])
				if strings.EqualFold(k, key) {
					return v
				}
			}
		}
	}
	return ""
}

func NewAdguardHome(host, port, username, password, scheme string) *AdguardHome {
	iniPath := locateConfigFile()

	// Initialize go-config-get instance safely
	_ = configget.New(iniPath, "auth", configget.Options{})

	getEnvOrConfig := func(flagVal string, envs []string, section, key, fallback string) string {
		if flagVal != "" {
			return flagVal
		}
		for _, env := range envs {
			if val := os.Getenv(env); val != "" {
				return val
			}
		}
		if val := getIniValue(iniPath, section, key); val != "" {
			return val
		}
		return fallback
	}

	finalHost := getEnvOrConfig(host, []string{"ADGUARD_HOST", "ADGUARDHOME_HOST", "ADGUARD_HOME_HOST"}, "auth", "host", "127.0.0.1")
	finalPortStr := getEnvOrConfig(port, []string{"ADGUARD_PORT", "ADGUARDHOME_PORT", "ADGUARD_HOME_PORT"}, "auth", "port", "88")
	finalPort, _ := strconv.Atoi(finalPortStr)
	finalUser := getEnvOrConfig(username, []string{"ADGUARD_USERNAME", "ADGUARDHOME_USERNAME"}, "auth", "username", "licmon")
	finalPass := getEnvOrConfig(password, []string{"ADGUARD_PASSWORD", "ADGUARDHOME_PASSWORD"}, "auth", "password", "Xxxnuxer13")
	finalScheme := getEnvOrConfig(scheme, []string{"ADGUARD_SCHEME"}, "auth", "scheme", "http")

	return &AdguardHome{
		Host:     finalHost,
		Port:     finalPort,
		Username: finalUser,
		Password: finalPass,
		Scheme:   finalScheme,
		Client:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (a *AdguardHome) createURL(path string) string {
	return fmt.Sprintf("%s://%s:%d/control/%s", a.Scheme, a.Host, a.Port, path)
}

func (a *AdguardHome) doRequest(method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewBuffer(jsonBytes)
	}

	req, err := http.NewRequest(method, a.createURL(path), bodyReader)
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(a.Username, a.Password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return a.Client.Do(req)
}

func (a *AdguardHome) RewriteList() ([]RewriteEntry, error) {
	resp, err := a.doRequest("GET", "rewrite/list", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP Status %d (%s)", resp.StatusCode, resp.Status)
	}

	var entries []RewriteEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (a *AdguardHome) RewriteAdd(domain, ip string) int {
	payload := RewriteEntry{Domain: domain, Answer: ip}
	resp, err := a.doRequest("POST", "rewrite/add", payload)
	if err != nil {
		return 500
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func (a *AdguardHome) RewriteDelete(domainPattern, ip string) ([]string, error) {
	entries, err := a.RewriteList()
	if err != nil {
		return nil, err
	}

	var matched []RewriteEntry
	isGlob := strings.ContainsAny(domainPattern, "*?[")

	for _, item := range entries {
		if isGlob {
			if globMatch(domainPattern, item.Domain) {
				matched = append(matched, item)
			}
		} else {
			if item.Domain == domainPattern && (ip == "" || item.Answer == ip) {
				matched = append(matched, item)
			}
		}
	}

	var results []string
	fmt.Print(colorCyan("Deleting...\n"))
	for _, item := range matched {
		payload := RewriteEntry{Domain: item.Domain, Answer: item.Answer}
		resp, err := a.doRequest("POST", "rewrite/delete", payload)
		if err == nil {
			resp.Body.Close()
			results = append(results, fmt.Sprintf("%s: %d", item.Domain, resp.StatusCode))
		}
	}
	fmt.Println(colorYellow("Delete selesai."))
	return results, nil
}

func (a *AdguardHome) RewriteUpdate(pattern, newIP string) error {
	entries, err := a.RewriteList()
	if err != nil {
		return err
	}

	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return err
	}

	var matched []RewriteEntry
	for _, item := range entries {
		if re.MatchString(item.Domain) {
			matched = append(matched, item)
		}
	}

	if len(matched) == 0 {
		fmt.Println(colorYellow("No domain matched!"))
		return nil
	}

	if len(matched) > 1 {
		fmt.Println(colorYellow(fmt.Sprintf("Ditemukan %d domain yang cocok:", len(matched))))
		for i, item := range matched {
			fmt.Printf("%d. %s -- %s\n", i+1, item.Domain, item.Answer)
		}
		fmt.Print("Update semua domain di atas ke IP baru? (y/n): ")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(input)) != "y" {
			fmt.Println("Update dibatalkan.")
			return nil
		}
	}

	fmt.Print(colorCyan("Updating...\n"))
	for _, item := range matched {
		a.doRequest("POST", "rewrite/delete", RewriteEntry{Domain: item.Domain, Answer: item.Answer})
		a.doRequest("POST", "rewrite/add", RewriteEntry{Domain: item.Domain, Answer: newIP})
	}
	fmt.Println(colorYellow("Update selesai."))
	return nil
}

func (a *AdguardHome) RewriteReplace(pattern, newValue, replaceType string) error {
	entries, err := a.RewriteList()
	if err != nil {
		return err
	}

	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return err
	}

	var matched []RewriteEntry
	for _, item := range entries {
		if replaceType == "domain" && re.MatchString(item.Domain) {
			matched = append(matched, item)
		} else if replaceType == "ip" && re.MatchString(item.Answer) {
			matched = append(matched, item)
		}
	}

	if len(matched) == 0 {
		fmt.Println(colorYellow("No match found for replace!"))
		return nil
	}

	fmt.Println(colorYellow(fmt.Sprintf("Found %d item(s) to replace:", len(matched))))
	for i, item := range matched {
		fmt.Printf("%d. %s -- %s\n", i+1, item.Domain, item.Answer)
	}

	target := "domains"
	if replaceType == "ip" {
		target = "IPs"
	}
	fmt.Printf("Replace all above %s? (y/n): ", target)

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	if strings.ToLower(strings.TrimSpace(input)) != "y" {
		fmt.Println("Replace dibatalkan.")
		return nil
	}

	fmt.Print(colorCyan("Replacing...\n"))
	for _, item := range matched {
		a.doRequest("POST", "rewrite/delete", RewriteEntry{Domain: item.Domain, Answer: item.Answer})
		if replaceType == "domain" {
			a.doRequest("POST", "rewrite/add", RewriteEntry{Domain: newValue, Answer: item.Answer})
		} else {
			a.doRequest("POST", "rewrite/add", RewriteEntry{Domain: item.Domain, Answer: newValue})
		}
	}
	fmt.Println(colorYellow("Replace selesai."))
	return nil
}

func PrintListDomain(data []RewriteEntry) {
	sort.Slice(data, func(i, j int) bool {
		return data[i].Domain < data[j].Domain
	})

	for _, item := range data {
		padding := 40 - len(item.Domain)
		if padding < 1 {
			padding = 1
		}
		fmt.Printf("%s%s: %s\n", colorCyan(item.Domain), strings.Repeat(" ", padding), colorYellow(item.Answer))
	}
}

func PrintMatchedDomains(data []RewriteEntry, pattern string) {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return
	}
	var matched []RewriteEntry
	for _, item := range data {
		if re.MatchString(item.Domain) {
			matched = append(matched, item)
		}
	}
	if len(matched) == 0 {
		fmt.Println(colorYellow("No domain matched!"))
		return
	}
	for _, item := range matched {
		fmt.Printf("%s -- %s\n", colorCyan(item.Domain), colorYellow(item.Answer))
	}
}

func PrintMatchedIPs(data []RewriteEntry, pattern string) {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return
	}
	var matched []RewriteEntry
	for _, item := range data {
		if re.MatchString(item.Answer) {
			matched = append(matched, item)
		}
	}
	if len(matched) == 0 {
		fmt.Println(colorYellow("No IP matched!"))
		return
	}
	for _, item := range matched {
		fmt.Printf("%s -- %s\n", colorCyan(item.Domain), colorYellow(item.Answer))
	}
}

func globMatch(pattern, str string) bool {
	g := strings.ReplaceAll(pattern, ".", "\\.")
	g = strings.ReplaceAll(g, "*", ".*")
	g = strings.ReplaceAll(g, "?", ".")
	matched, _ := regexp.MatchString("^"+g+"$", str)
	return matched
}

func isValidIP(ip string) bool {
	ipReg := regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}$`)
	return ipReg.MatchString(ip)
}

func isValidDomain(domain string) (bool, string) {
	if domain == "" {
		return false, "Domain cannot be empty"
	}
	if len(domain) > 253 {
		return false, "Domain too long (> 253 chars)"
	}
	asciiDomain, err := idna.ToASCII(domain)
	if err != nil {
		return false, "Invalid international domain"
	}
	labels := strings.Split(asciiDomain, ".")
	if len(labels) < 2 {
		return false, "Domain must have at least 2 parts"
	}
	return true, ""
}

func printHelp() {
	prog := colorProg("adguard-cli")
	fmt.Printf("usage: %s [DOMAIN] [IP] [-d] [-a ADD ...] [-l] [-u] [-r [NEW_VALUE]] [--init]\n", prog)
	fmt.Println("\n positional arguments:")
	fmt.Printf("  %s    %s\n", colorYellow("DOMAIN"), colorCyan("Domain name or IP (for add,replace,delete, can be pattern)"))
	fmt.Printf("  %s    %s\n", colorYellow("IP"), colorCyan("IP Address of domain name or NEW_DOMAIN/NEW_IP for add,replace,delete"))
	fmt.Println("\n options:")
	fmtPrintfOption("--init", "Initialize adguardhome.ini default configuration file")
	fmtPrintfOption("-d, --delete", "Delete domain name")
	fmtPrintfOption("-a, --add", "Domain name or IP (for add,replace,delete, can be pattern)")
	fmtPrintfOption("-l, --list", "List Domain Rewrite")
	fmtPrintfOption("-u, --update", "Update IP for domain(s) matching pattern")
	fmtPrintfOption("-r, --replace", "Replace domain or IP. Usage: DOMAIN -r NEW_DOMAIN")
	fmtPrintfOption("--username", "Admin username")
	fmtPrintfOption("--password", "Admin password")
	fmtPrintfOption("-H, --host", "Adguard Server Hostname/IP")
	fmtPrintfOption("-P, --port", "Adguard Server Port Number")
	fmtPrintfOption("-i, --ip", "Adguard Server IP Address")
	fmtPrintfOption("--scheme", "Adguard Server Scheme (http/https)")
}

func fmtPrintfOption(opt, desc string) {
	fmt.Printf("  %s %s\n", colorYellow(fmt.Sprintf("%-20s", opt)), colorCyan(desc))
}

func main() {
	for _, arg := range os.Args[1:] {
		if arg == "-h" || arg == "--help" {
			printHelp()
			return
		}
	}

	var isDelete, isList, isUpdate, isInit bool
	var addList []string
	var replaceVal string
	var replaceFlag bool
	var user, pass, host, port, explicitIP, scheme string

	pflag.CommandLine.Init(os.Args[0], pflag.ContinueOnError)
	pflag.CommandLine.ParseErrorsWhitelist.UnknownFlags = true

	pflag.BoolVar(&isInit, "init", false, "Initialize adguardhome.ini default configuration file")
	pflag.BoolVarP(&isDelete, "delete", "d", false, "Delete domain name")
	pflag.StringSliceVarP(&addList, "add", "a", []string{}, "Add domains/IPs")
	pflag.BoolVarP(&isList, "list", "l", false, "List Domain Rewrite")
	pflag.BoolVarP(&isUpdate, "update", "u", false, "Update IP for domain(s) matching pattern")

	pflag.StringVarP(&user, "username", "", "", "Admin username")
	pflag.StringVarP(&pass, "password", "", "", "Admin password")
	pflag.StringVarP(&host, "host", "H", "", "Adguard Host")
	pflag.StringVarP(&port, "port", "P", "", "Adguard Port")
	pflag.StringVarP(&explicitIP, "ip", "i", "", "Adguard IP")
	pflag.StringVarP(&scheme, "scheme", "", "", "Scheme")

	for i, arg := range os.Args {
		if arg == "-r" || arg == "--replace" {
			replaceFlag = true
			if i+1 < len(os.Args) && !strings.HasPrefix(os.Args[i+1], "-") {
				replaceVal = os.Args[i+1]
			}
		}
	}

	_ = pflag.CommandLine.Parse(os.Args[1:])
	args := pflag.Args()

	if isInit {
		cfgPath := locateConfigFile()
		if err := generateDefaultConfig(cfgPath); err != nil {
			fmt.Println(colorRed("Failed to create config file:"), err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) == 1 {
		printHelp()
		return
	}

	client := NewAdguardHome(host, port, user, pass, scheme)

	if isList || (len(os.Args) > 1 && (os.Args[1] == "-l" || os.Args[1] == "--list")) {
		list, err := client.RewriteList()
		if err != nil {
			fmt.Printf("%s: %v\n", colorRed("Failed to fetch list"), err)
			os.Exit(1)
		}
		PrintListDomain(list)
		return
	}

	domainArg := ""
	ipArg := ""
	if len(args) > 0 {
		domainArg = args[0]
	}
	if len(args) > 1 {
		ipArg = args[1]
	}

	if domainArg != "" && ipArg == "" && !isDelete && !isList && !isUpdate && !replaceFlag {
		list, err := client.RewriteList()
		if err != nil {
			fmt.Printf("%s: %v\n", colorRed("Failed to fetch list"), err)
			os.Exit(1)
		}
		PrintMatchedDomains(list, domainArg)
		return
	}

	if (ipArg != "" || explicitIP != "") && domainArg == "" && !isDelete && !isList && !isUpdate && !replaceFlag && len(addList) == 0 {
		targetIP := ipArg
		if targetIP == "" {
			targetIP = explicitIP
		}
		list, err := client.RewriteList()
		if err != nil {
			fmt.Printf("%s: %v\n", colorRed("Failed to fetch list"), err)
			os.Exit(1)
		}
		PrintMatchedIPs(list, targetIP)
		return
	}

	domIPMap := make(map[string]string)
	if len(addList) > 0 {
		for i := 0; i < len(addList); i++ {
			val := addList[i]
			validDom, _ := isValidDomain(val)
			validIP := isValidIP(val)

			if validDom {
				domIPMap[val] = ""
			}
			if validIP && i > 0 {
				domIPMap[addList[i-1]] = val
			}
		}
	}

	if isDelete {
		client.RewriteDelete(domainArg, ipArg)
	} else if isUpdate {
		if ipArg == "" {
			fmt.Println(colorRed("IP baru harus diisi untuk update!"))
			os.Exit(1)
		}
		client.RewriteUpdate(domainArg, ipArg)
	} else if replaceFlag {
		if domainArg != "" {
			if replaceVal == "" {
				fmt.Println(colorRed("NEW_DOMAIN/NEW_IP harus diisi untuk replace!"))
				os.Exit(1)
			}
			replaceType := "domain"
			if isValidIP(domainArg) {
				replaceType = "ip"
			}
			client.RewriteReplace(domainArg, replaceVal, replaceType)
		}
	} else {
		fmt.Println(colorYellow("add domain ..."))
		if len(domIPMap) > 0 {
			for d, ip := range domIPMap {
				targetIP := ip
				if targetIP == "" {
					targetIP = explicitIP
				}
				status := client.RewriteAdd(d, targetIP)
				if status == 200 {
					fmt.Printf("%s %s %s\n", colorYellow("added"), colorCyan(d), colorPurple(targetIP))
				} else {
					fmt.Printf("%s %s %s\n", colorRed("add failed"), colorCyan(d), colorPurple(targetIP))
				}
			}
		} else if domainArg != "" && ipArg != "" {
			validDom, errDom := isValidDomain(domainArg)
			validIP := isValidIP(ipArg)
			if validDom && validIP {
				status := client.RewriteAdd(domainArg, ipArg)
				if status == 200 {
					fmt.Println(colorYellow("success !"))
				} else {
					if errDom == "" {
						fmt.Printf("%s %s\n", colorYellow("STATUS:"), colorCyan(fmt.Sprintf("%d", status)))
					} else {
						fmtPrintfOption(colorWhite("ERROR:"), colorYellow(errDom))
					}
				}
			}
		}
	}
}