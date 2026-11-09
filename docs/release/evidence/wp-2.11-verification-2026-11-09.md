# WP-2.11 Tax returns and e-invoicing: verification, 2026-11-09

SRS: FIN-07, FIN-14, RPT-08. Migration `0063_tax.sql`; `internal/kernel/tax.go`; `internal/httpapi/tax_handlers.go`;
console **Accounting → Tax returns & e-invoices** (Returns, E-invoices, Tax accounts). Permissions `tax.prepare`
(accountant) and `tax.approve` (CFO); duty-conflict pair "Tax returns".

## Controls
Tax accounts are typed (output VAT and withholding payable are liabilities, input VAT an asset). A VAT or WHT return for a
period holds lines computed by the database from posted journals (`tax_return_compute`), checked at commit; a forged line is
refused. One live return per entity, type and period; approval by the CFO with fresh MFA, not the preparer; filing needs the
authority's reference; approved, filed, rejected and cancelled returns never change. E-invoices: only an approved customer
invoice, accepted at most once, with the payload hash stored; the adapter is a stand-in (decision D-06) that rejects a customer
without a TIN.

## Test (TestTax_VATReturnAndEInvoice)
Real posted journals: input VAT 222,975 from an approved supplier invoice and output VAT from an approved client invoice; the
return shows both and nets them. Also proven: wrong account type refused; a QS cannot designate or read; a return before any
designation refused; duplicate period refused; forged line refused; unapproved return cannot be filed; preparer cannot approve;
stale MFA refused; filing without a reference refused; a filed return cannot be edited or cancelled; a WHT return can be
rejected; e-invoice accepted once, second refused, QS refused; fake adapter rejects a missing TIN.

## Browser journey (TestConsoleBrowserFinance)
Accountant designates accounts, prepares a VAT return, CFO approves, accountant files; the controller opens the consolidation
runs, run sheet, groups and intercompany tabs; accessibility audited incl. 390 px dark.

## Not built
Automatic withholding on supplier payments and customer receipts, numbered withholding certificates, return forms in the
authority's layout, tax rule engine hookup to the returns, e-invoice format (D-06).
