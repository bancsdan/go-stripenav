package nav

import "encoding/xml"

// The request types here are the on-wire envelopes the client sends. They
// share the OSA/3.0/api default namespace and the NTCA/1.0/common prefix
// for the header, user, and (where applicable) basic response wrappers.

type tokenExchangeRequest struct {
	XMLName  xml.Name        `xml:"http://schemas.nav.gov.hu/OSA/3.0/api TokenExchangeRequest"`
	XmlnsCom string          `xml:"xmlns:common,attr"`
	Header   commonHeaderXML `xml:"common:header"`
	User     commonUserXML   `xml:"common:user"`
	Software softwareXML     `xml:"software"`
}

type manageInvoiceRequest struct {
	XMLName       xml.Name             `xml:"http://schemas.nav.gov.hu/OSA/3.0/api ManageInvoiceRequest"`
	XmlnsCom      string               `xml:"xmlns:common,attr"`
	Header        commonHeaderXML      `xml:"common:header"`
	User          commonUserXML        `xml:"common:user"`
	Software      softwareXML          `xml:"software"`
	ExchangeToken string               `xml:"exchangeToken"`
	Operations    invoiceOperationsXML `xml:"invoiceOperations"`
}

type invoiceOperationsXML struct {
	CompressedContent bool                  `xml:"compressedContent"`
	Operations        []invoiceOperationXML `xml:"invoiceOperation"`
}

type invoiceOperationXML struct {
	Index             int    `xml:"index"`
	InvoiceOperation  string `xml:"invoiceOperation"` // CREATE, MODIFY, STORNO
	InvoiceData       string `xml:"invoiceData"`      // base64-encoded InvoiceData XML
}

type manageAnnulmentRequest struct {
	XMLName         xml.Name                `xml:"http://schemas.nav.gov.hu/OSA/3.0/api ManageAnnulmentRequest"`
	XmlnsCom        string                  `xml:"xmlns:common,attr"`
	Header          commonHeaderXML         `xml:"common:header"`
	User            commonUserXML           `xml:"common:user"`
	Software        softwareXML             `xml:"software"`
	ExchangeToken   string                  `xml:"exchangeToken"`
	Annulments      annulmentOperationsXML  `xml:"annulmentOperations"`
}

type annulmentOperationsXML struct {
	Annulments []annulmentOperationXML `xml:"annulmentOperation"`
}

type annulmentOperationXML struct {
	Index            int    `xml:"index"`
	AnnulmentOperation string `xml:"annulmentOperation"` // ANNUL
	InvoiceAnnulment string `xml:"invoiceAnnulment"`   // base64-encoded InvoiceAnnulment XML
}

type queryTransactionStatusRequest struct {
	XMLName               xml.Name        `xml:"http://schemas.nav.gov.hu/OSA/3.0/api QueryTransactionStatusRequest"`
	XmlnsCom              string          `xml:"xmlns:common,attr"`
	Header                commonHeaderXML `xml:"common:header"`
	User                  commonUserXML   `xml:"common:user"`
	Software              softwareXML     `xml:"software"`
	TransactionID         string          `xml:"transactionId"`
	ReturnOriginalRequest bool            `xml:"returnOriginalRequest"`
}

const xmlnsCommonURI = "http://schemas.nav.gov.hu/NTCA/1.0/common"
