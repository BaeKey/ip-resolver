package model

import (
	"fmt"
	"strings"
)

type IPInfo struct {
	Province string `json:"province"`
	ISP      string `json:"isp"`
	ProvinceCode string `json:"province_code"`
	ISPCode      string `json:"isp_code"`
}

type trieNode struct {
	children map[rune]*trieNode
	code     string
}

func newTrieNode() *trieNode {
	return &trieNode{
		children: make(map[rune]*trieNode),
	}
}

func (t *trieNode) insert(key, code string) {
	node := t
	for _, r := range key {
		if node.children[r] == nil {
			node.children[r] = newTrieNode()
		}
		node = node.children[r]
	}
	node.code = code
}

func (t *trieNode) matchPrefix(input string) string {
	node := t
	for _, r := range input {
		next, ok := node.children[r]
		if !ok {
			return ""
		}
		node = next
		if node.code != "" {
			return node.code
		}
	}
	return ""
}

var provinceTrieRoot = newTrieNode()

func init() {
	cnMap := map[string]string{
		"北京": "beijing", "天津": "tianjin", "河北": "hebei", "山西": "shanxi",
		"内蒙古": "neimenggu", "辽宁": "liaoning", "吉林": "jilin", "黑龙江": "heilongjiang",
		"上海": "shanghai", "江苏": "jiangsu", "浙江": "zhejiang", "安徽": "anhui",
		"福建": "fujian", "江西": "jiangxi", "山东": "shandong", "河南": "henan",
		"湖北": "hubei", "湖南": "hunan", "广东": "guangdong", "广西": "guangxi",
		"海南": "hainan", "重庆": "chongqing", "四川": "sichuan", "贵州": "guizhou",
		"云南": "yunnan", "西藏": "xizang", "陕西": "shaanxi", "甘肃": "gansu",
		"青海": "qinghai", "宁夏": "ningxia", "新疆": "xinjiang",
		"香港": "hk", "澳门": "mo", "台湾": "tw",
	}

	for k, v := range cnMap {
		provinceTrieRoot.insert(k, v)
		provinceTrieRoot.insert(v, v)
	}
}

type ispRule struct {
	Code     string
	Keywords []string
}

var ispRules = []ispRule{
	{Code: "ct", Keywords: []string{"电信", "TELECOM", "CHINANET"}},
	{Code: "cu", Keywords: []string{"联通", "UNICOM"}},
	{Code: "cmcc", Keywords: []string{"移动", "MOBILE", "TIETONG", "铁通"}},
	{Code: "edu", Keywords: []string{"教育", "EDU", "CERNET"}},
	{Code: "gwbn", Keywords: []string{"长城", "GWBN"}},
	{Code: "cbn", Keywords: []string{"广电", "CABLE", "CBN"}},
}

func (i *IPInfo) Standardize() {
	i.detectProvinceCode()
	i.detectISPCode()
}

func (i *IPInfo) detectProvinceCode() {
	raw := strings.TrimSpace(i.Province)
	if raw == "" {
		return
	}

	key := strings.ToLower(raw)

	if code := provinceTrieRoot.matchPrefix(key); code != "" {
		i.ProvinceCode = code
	}
}

func (i *IPInfo) detectISPCode() {
	raw := strings.ToUpper(strings.TrimSpace(i.ISP))
	if raw == "" {
		return
	}

	for _, rule := range ispRules {
		for _, kw := range rule.Keywords {
			if strings.Contains(raw, kw) {
				i.ISPCode = rule.Code
				return
			}
		}
	}
}

func (i *IPInfo) ToTag() string {
	if i.ProvinceCode == "" || i.ISPCode == "" {
		return "fallback"
	}
	return fmt.Sprintf("%s_%s", i.ProvinceCode, i.ISPCode)
}
