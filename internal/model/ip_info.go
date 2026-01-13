package model

import (
	"fmt"
	"strings"
)

// IPInfo 原始数据结构
type IPInfo struct {
	Province string `json:"province"`
	ISP      string `json:"isp"`
}

// 映射表：中文 -> 拼音/代码
var provinceMap = map[string]string{
	"北京市": "beijing", "天津市": "tianjin", "河北省": "hebei", "山西省": "shanxi",
	"内蒙古自治区": "neimenggu", "内蒙古": "neimenggu",
	"辽宁省": "liaoning", "吉林省": "jilin", "黑龙江省": "heilongjiang",
	"上海市": "shanghai", "江苏省": "jiangsu", "浙江省": "zhejiang", "安徽省": "anhui",
	"福建省": "fujian", "江西省": "jiangxi", "山东省": "shandong",
	"河南省": "henan", "湖北省": "hubei", "湖南省": "hunan", "广东省": "guangdong",
	"广西壮族自治区": "guangxi", "广西": "guangxi",
	"海南省": "hainan", "重庆市": "chongqing", "四川省": "sichuan", "贵州省": "guizhou",
	"云南省": "yunnan", "西藏自治区": "xizang", "西藏": "xizang",
	"陕西省": "shaanxi", "甘肃省": "gansu", "青海省": "qinghai",
	"宁夏回族自治区": "ningxia", "宁夏": "ningxia",
	"新疆维吾尔自治区": "xinjiang", "新疆": "xinjiang",
	"香港": "hk", "澳门": "mo", "台湾": "tw",
}

var ispMap = map[string]string{
	"中国电信": "ct",   // China Telecom
	"中国移动": "cmcc", // China Mobile
	"中国联通": "cu",   // China Unicom
	"教育网":   "edu",  // CERNET
	"长城宽带": "gwbn",
	"广电网":   "cbn",
	"铁通":    "cmcc", // 归并到移动
}

// Standardize 清洗数据：统一 ISP 名称和省份后缀
func (i *IPInfo) Standardize() {
	i.standardizeISP()
	i.standardizeProvince()
}

// 如果省份或 ISP 任意一个无法识别，直接返回 "fallback"
func (i *IPInfo) ToTag() string {
	provCode, okProv := provinceMap[i.Province]
	ispCode, okISP := ispMap[i.ISP]

	// 双重校验: 只要有一个字段不在映射表中，返回 fallback
	if !okProv || !okISP {
		return "fallback"
	}

	return fmt.Sprintf("%s_%s", provCode, ispCode)
}

func (i *IPInfo) standardizeISP() {
	raw := strings.ToUpper(i.ISP)

	if strings.Contains(raw, "电信") || strings.Contains(raw, "TELECOM") || strings.Contains(raw, "CHINANET") {
		i.ISP = "中国电信"
		return
	}
	if strings.Contains(raw, "联通") || strings.Contains(raw, "UNICOM") {
		i.ISP = "中国联通"
		return
	}
	if strings.Contains(raw, "移动") || strings.Contains(raw, "MOBILE") || strings.Contains(raw, "TIETONG") || strings.Contains(raw, "铁通") {
		i.ISP = "中国移动"
		return
	}
	if strings.Contains(raw, "教育") || strings.Contains(raw, "EDU") {
		i.ISP = "教育网"
		return
	}
	if strings.Contains(raw, "长城") || strings.Contains(raw, "GWBN") {
		i.ISP = "长城宽带"
		return
	}
	if strings.Contains(raw, "广电") || strings.Contains(raw, "CABLE") {
		i.ISP = "广电网"
		return
	}
}

func (i *IPInfo) standardizeProvince() {
	if i.Province == "" {
		return
	}
	name := strings.TrimSpace(i.Province)

	// 特殊行政区处理
	specialRegions := []string{"北京", "上海", "天津", "重庆", "内蒙古", "广西", "西藏", "宁夏", "新疆", "香港", "澳门", "台湾"}
	isSpecial := false
	for _, r := range specialRegions {
		if strings.HasPrefix(name, r) {
			isSpecial = true
			break
		}
	}

	if !isSpecial && !strings.HasSuffix(name, "省") {
		name += "省"
	}
	if (name == "北京" || name == "上海" || name == "天津" || name == "重庆") && !strings.HasSuffix(name, "市") {
		name += "市"
	}
	i.Province = name
}