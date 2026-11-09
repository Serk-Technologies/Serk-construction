# Role and permission matrix

Generated from `pkg/auth/auth.go` (`DefaultRoles`); do not edit by hand. Regenerate with
`UPDATE_ROLE_DOC=1 go test ./pkg/auth -run TestRoleMatrixDocumentIsCurrent`.

A tenant may define its own roles from the same permission list; these are the seed roles. A role never holds both halves of a control:
the conflict catalogue below is tested against every role.

## Roles and what they may do

### accountant

`asset.manage`, `bank.reconcile`, `equipment.charge`, `fx.read`, `intelligence.read`, `journal.create`, `journal.read`, `p2p.read`, `scope.tenant_wide`, `tax.prepare`, `workflow.read`

### ap_clerk

`ap.enter`, `p2p.read`

### ap_supervisor

`ap.approve`, `p2p.read`

### ar_clerk

`ar.credit.create`, `ar.invoice.create`, `ar.receipt.create`, `ar.retention.create`, `customer.manage`, `p2p.read`, `scope.tenant_wide`

### ar_supervisor

`ar.invoice.approve`, `ar.receipt.confirm`, `p2p.read`, `scope.tenant_wide`

### auditor

`audit.read`, `fx.read`, `intelligence.read`, `journal.read`, `p2p.read`, `scope.tenant_wide`, `workflow.read`

### bank_statement_officer

`bank.import`, `p2p.read`, `scope.tenant_wide`

### cfo

`approval.high_value`, `asset.approve`, `audit.read`, `authority.approve`, `bank.details.reveal`, `bankacct.approve`, `budget.approve`, `budget.override`, `consolidation.approve`, `contract.approve`, `contract.policy.approve`, `delegation.create`, `forecast.approve`, `fx.approve_exception`, `fx.approve`, `fx.policy.approve`, `fx.read`, `intelligence.read`, `journal.read`, `p2p.policy.approve`, `p2p.read`, `payment.approve`, `period.reopen`, `project.gate.decide`, `scope.tenant_wide`, `tax.approve`, `workflow.publish`, `workflow.read`

### commercial_manager

`ar.credit.approve`, `ar.retention.approve`, `bond.approve`, `boq.approve`, `budget.approve`, `change.approve`, `claim.approve`, `contract.manage`, `delegation.create`, `escalation.approve`, `forecast.approve`, `intelligence.read`, `ipc.certify`, `p2p.read`, `project.create`, `subcontract.approve`, `subcontract.certify`

### controller

`account.manage`, `consolidation.manage`, `contract.policy.manage`, `delegation.create`, `fx.read`, `intelligence.read`, `journal.post`, `journal.read`, `journal.reverse`, `org.manage`, `p2p.policy.manage`, `p2p.read`, `period.close`, `scope.tenant_wide`, `workflow.read`

### document_controller

`doc.manage`, `doc.raise`, `p2p.read`, `scope.tenant_wide`, `workflow.read`

### estimator

`boq.manage`, `p2p.read`

### finance_manager

`ap.approve_exception`, `bank.details.reveal`, `customer.approve`, `delegation.create`, `fx.approve`, `fx.policy.approve`, `fx.read`, `intelligence.read`, `journal.approve`, `journal.read`, `p2p.read`, `payment.approve`, `scope.tenant_wide`, `stock.item.approve`, `supplier.bank.approve`, `wbs.approve`, `workflow.read`

### hr_manager

`payroll.approve`, `scope.tenant_wide`, `workflow.read`

### hse_officer

`hse.manage`, `hse.report`, `p2p.read`, `scope.tenant_wide`, `workflow.read`

### inventory_controller

`p2p.read`, `scope.tenant_wide`, `stock.adjust.approve`, `stock.transfer.approve`

### payroll_officer

`payroll.prepare`, `scope.tenant_wide`, `workflow.read`

### planner

`credit.manage`, `intelligence.read`, `p2p.read`, `schedule.manage`

### plant_manager

`equipment.manage`, `equipment.record`, `p2p.read`, `scope.tenant_wide`, `workflow.read`

### process_owner

`authority.manage`, `automation.manage`, `intelligence.read`, `scope.tenant_wide`, `workflow.define`, `workflow.read`

### procurement_manager

`delegation.create`, `intelligence.read`, `p2p.read`, `po.approve`, `rfq.award`, `supplier.approve`, `warehouse.manage`

### procurement_officer

`p2p.read`, `po.manage`, `rfq.manage`, `stock.item.manage`, `supplier.manage`

### project_accountant

`fx.read`, `journal.create`, `journal.read`, `revenue.run`, `workflow.read`

### project_manager

`budget.revise`, `budget.transfer`, `change.manage`, `claim.manage`, `credit.approve`, `delegation.create`, `doc.approve`, `doc.raise`, `doc.respond`, `equipment.approve`, `forecast.prepare`, `fx.read`, `hse.report`, `intelligence.read`, `journal.read`, `p2p.read`, `pr.approve`, `project.gate.submit`, `project.update`, `quality.approve`, `quality.request`, `schedule.approve`, `site.diary.approve`, `site.instruct`, `site.record`, `stock.issue.approve`, `stock.reserve`, `timesheet.approve`, `wbs.manage`, `workflow.read`

### quality_engineer

`p2p.read`, `quality.manage`, `quality.request`, `scope.tenant_wide`, `site.record`, `workflow.read`

### quantity_surveyor

`change.manage`, `claim.manage`, `escalation.manage`, `intelligence.read`, `ipc.prepare`, `measure.create`, `p2p.read`, `subcontract.apply`, `subcontract.manage`

### security_admin

`audit.read`, `delegation.approve`, `identity.revoke_sessions`, `role.manage`, `scope.tenant_wide`, `site.device.manage`, `tenant.settings.manage`, `user.manage`

### site_engineer

`credit.record`, `doc.raise`, `equipment.record`, `grn.confirm`, `hse.report`, `measure.verify`, `p2p.read`, `pr.create`, `quality.request`, `rfq.evaluate`, `site.device.register`, `site.diary.manage`, `site.record`, `stock.issue.approve`, `stock.reserve`, `timesheet.record`

### storekeeper

`grn.create`, `p2p.read`, `stock.count`, `stock.issue`, `stock.receive`, `stock.reserve`, `stock.transfer`

### treasury_officer

`bankacct.manage`, `bond.manage`, `fx.manage`, `fx.read`, `intelligence.read`, `p2p.read`, `payment.prepare`, `payment.release`, `scope.tenant_wide`, `workflow.read`

### workflow_admin

`automation.activate`, `intelligence.read`, `scope.tenant_wide`, `workflow.admin`, `workflow.read`

## Who holds each permission

| Permission | Roles |
|---|---|
| `account.manage` | controller |
| `ap.approve` | ap_supervisor |
| `ap.approve_exception` | finance_manager |
| `ap.enter` | ap_clerk |
| `approval.high_value` | cfo |
| `ar.credit.approve` | commercial_manager |
| `ar.credit.create` | ar_clerk |
| `ar.invoice.approve` | ar_supervisor |
| `ar.invoice.create` | ar_clerk |
| `ar.receipt.confirm` | ar_supervisor |
| `ar.receipt.create` | ar_clerk |
| `ar.retention.approve` | commercial_manager |
| `ar.retention.create` | ar_clerk |
| `asset.approve` | cfo |
| `asset.manage` | accountant |
| `audit.read` | auditor, cfo, security_admin |
| `authority.approve` | cfo |
| `authority.manage` | process_owner |
| `automation.activate` | workflow_admin |
| `automation.manage` | process_owner |
| `bank.details.reveal` | cfo, finance_manager |
| `bank.import` | bank_statement_officer |
| `bank.reconcile` | accountant |
| `bankacct.approve` | cfo |
| `bankacct.manage` | treasury_officer |
| `bond.approve` | commercial_manager |
| `bond.manage` | treasury_officer |
| `boq.approve` | commercial_manager |
| `boq.manage` | estimator |
| `budget.approve` | cfo, commercial_manager |
| `budget.override` | cfo |
| `budget.revise` | project_manager |
| `budget.transfer` | project_manager |
| `change.approve` | commercial_manager |
| `change.manage` | project_manager, quantity_surveyor |
| `claim.approve` | commercial_manager |
| `claim.manage` | project_manager, quantity_surveyor |
| `consolidation.approve` | cfo |
| `consolidation.manage` | controller |
| `contract.approve` | cfo |
| `contract.manage` | commercial_manager |
| `contract.policy.approve` | cfo |
| `contract.policy.manage` | controller |
| `credit.approve` | project_manager |
| `credit.manage` | planner |
| `credit.record` | site_engineer |
| `customer.approve` | finance_manager |
| `customer.manage` | ar_clerk |
| `delegation.approve` | security_admin |
| `delegation.create` | cfo, commercial_manager, controller, finance_manager, procurement_manager, project_manager |
| `doc.approve` | project_manager |
| `doc.manage` | document_controller |
| `doc.raise` | document_controller, project_manager, site_engineer |
| `doc.respond` | project_manager |
| `equipment.approve` | project_manager |
| `equipment.charge` | accountant |
| `equipment.manage` | plant_manager |
| `equipment.record` | plant_manager, site_engineer |
| `escalation.approve` | commercial_manager |
| `escalation.manage` | quantity_surveyor |
| `forecast.approve` | cfo, commercial_manager |
| `forecast.prepare` | project_manager |
| `fx.approve` | cfo, finance_manager |
| `fx.approve_exception` | cfo |
| `fx.manage` | treasury_officer |
| `fx.policy.approve` | cfo, finance_manager |
| `fx.read` | accountant, auditor, cfo, controller, finance_manager, project_accountant, project_manager, treasury_officer |
| `grn.confirm` | site_engineer |
| `grn.create` | storekeeper |
| `hse.manage` | hse_officer |
| `hse.report` | hse_officer, project_manager, site_engineer |
| `identity.revoke_sessions` | security_admin |
| `intelligence.read` | accountant, auditor, cfo, commercial_manager, controller, finance_manager, planner, process_owner, procurement_manager, project_manager, quantity_surveyor, treasury_officer, workflow_admin |
| `ipc.certify` | commercial_manager |
| `ipc.prepare` | quantity_surveyor |
| `journal.approve` | finance_manager |
| `journal.create` | accountant, project_accountant |
| `journal.post` | controller |
| `journal.read` | accountant, auditor, cfo, controller, finance_manager, project_accountant, project_manager |
| `journal.reverse` | controller |
| `measure.create` | quantity_surveyor |
| `measure.verify` | site_engineer |
| `org.manage` | controller |
| `p2p.policy.approve` | cfo |
| `p2p.policy.manage` | controller |
| `p2p.read` | accountant, ap_clerk, ap_supervisor, ar_clerk, ar_supervisor, auditor, bank_statement_officer, cfo, commercial_manager, controller, document_controller, estimator, finance_manager, hse_officer, inventory_controller, planner, plant_manager, procurement_manager, procurement_officer, project_manager, quality_engineer, quantity_surveyor, site_engineer, storekeeper, treasury_officer |
| `payment.approve` | cfo, finance_manager |
| `payment.prepare` | treasury_officer |
| `payment.release` | treasury_officer |
| `payroll.approve` | hr_manager |
| `payroll.prepare` | payroll_officer |
| `period.close` | controller |
| `period.reopen` | cfo |
| `po.approve` | procurement_manager |
| `po.manage` | procurement_officer |
| `pr.approve` | project_manager |
| `pr.create` | site_engineer |
| `project.create` | commercial_manager |
| `project.gate.decide` | cfo |
| `project.gate.submit` | project_manager |
| `project.update` | project_manager |
| `quality.approve` | project_manager |
| `quality.manage` | quality_engineer |
| `quality.request` | project_manager, quality_engineer, site_engineer |
| `revenue.run` | project_accountant |
| `rfq.award` | procurement_manager |
| `rfq.evaluate` | site_engineer |
| `rfq.manage` | procurement_officer |
| `role.manage` | security_admin |
| `schedule.approve` | project_manager |
| `schedule.manage` | planner |
| `scope.tenant_wide` | accountant, ar_clerk, ar_supervisor, auditor, bank_statement_officer, cfo, controller, document_controller, finance_manager, hr_manager, hse_officer, inventory_controller, payroll_officer, plant_manager, process_owner, quality_engineer, security_admin, treasury_officer, workflow_admin |
| `site.device.manage` | security_admin |
| `site.device.register` | site_engineer |
| `site.diary.approve` | project_manager |
| `site.diary.manage` | site_engineer |
| `site.instruct` | project_manager |
| `site.record` | project_manager, quality_engineer, site_engineer |
| `stock.adjust.approve` | inventory_controller |
| `stock.count` | storekeeper |
| `stock.issue` | storekeeper |
| `stock.issue.approve` | project_manager, site_engineer |
| `stock.item.approve` | finance_manager |
| `stock.item.manage` | procurement_officer |
| `stock.receive` | storekeeper |
| `stock.reserve` | project_manager, site_engineer, storekeeper |
| `stock.transfer` | storekeeper |
| `stock.transfer.approve` | inventory_controller |
| `subcontract.apply` | quantity_surveyor |
| `subcontract.approve` | commercial_manager |
| `subcontract.certify` | commercial_manager |
| `subcontract.manage` | quantity_surveyor |
| `supplier.approve` | procurement_manager |
| `supplier.bank.approve` | finance_manager |
| `supplier.manage` | procurement_officer |
| `tax.approve` | cfo |
| `tax.prepare` | accountant |
| `tenant.settings.manage` | security_admin |
| `timesheet.approve` | project_manager |
| `timesheet.record` | site_engineer |
| `user.manage` | security_admin |
| `warehouse.manage` | procurement_manager |
| `wbs.approve` | finance_manager |
| `wbs.manage` | project_manager |
| `workflow.admin` | workflow_admin |
| `workflow.define` | process_owner |
| `workflow.publish` | cfo |
| `workflow.read` | accountant, auditor, cfo, controller, document_controller, finance_manager, hr_manager, hse_officer, payroll_officer, plant_manager, process_owner, project_accountant, project_manager, quality_engineer, treasury_officer, workflow_admin |

## Duties that must be kept apart (no role, and no person, may hold both)

| Control | Permissions |
|---|---|
| Journals: prepare, approve, post | `journal.create` + `journal.approve` + `journal.post` |
| FX rates | `fx.manage` + `fx.approve` |
| FX policy | `fx.manage` + `fx.policy.approve` |
| Workflow definitions | `workflow.define` + `workflow.publish` |
| Period close and reopen | `period.close` + `period.reopen` |
| WBS and cost codes | `wbs.manage` + `wbs.approve` |
| Supplier master | `supplier.manage` + `supplier.approve` |
| Supplier bank accounts | `supplier.manage` + `supplier.bank.approve` |
| Company bank accounts | `bankacct.manage` + `bankacct.approve` |
| BOQ | `boq.manage` + `boq.approve` |
| Requisitions | `pr.create` + `pr.approve` |
| RFQ: manage, award | `rfq.manage` + `rfq.award` |
| RFQ: manage, evaluate | `rfq.manage` + `rfq.evaluate` |
| Purchase orders | `po.manage` + `po.approve` |
| Goods receipt | `grn.create` + `grn.confirm` |
| Supplier invoices | `ap.enter` + `ap.approve` |
| Invoice price exceptions | `ap.enter` + `ap.approve_exception` |
| Payments: prepare, approve | `payment.prepare` + `payment.approve` |
| Payments: approve, release | `payment.approve` + `payment.release` |
| Payments: release, reconcile | `payment.release` + `bank.reconcile` |
| P2P policy | `p2p.policy.manage` + `p2p.policy.approve` |
| Bank statements and payments | `bank.import` + `bank.reconcile` |
| Bank import and payment prepare | `bank.import` + `payment.prepare` |
| Bank import and payment approve | `bank.import` + `payment.approve` |
| Bank import and payment release | `bank.import` + `payment.release` |
| Customers | `customer.manage` + `customer.approve` |
| Contract policy | `contract.policy.manage` + `contract.policy.approve` |
| Contracts | `contract.manage` + `contract.approve` |
| Measurements | `measure.create` + `measure.verify` |
| Payment certificates | `ipc.prepare` + `ipc.certify` |
| Customer invoices | `ar.invoice.create` + `ar.invoice.approve` |
| Customer receipts | `ar.receipt.create` + `ar.receipt.confirm` |
| Credit notes | `ar.credit.create` + `ar.credit.approve` |
| Retention | `ar.retention.create` + `ar.retention.approve` |
| Stock transfers | `stock.transfer` + `stock.transfer.approve` |
| Schedules | `schedule.manage` + `schedule.approve` |
| Bank import and receipts | `bank.import` + `ar.receipt.confirm` |
| Bank import and receipt creation | `bank.import` + `ar.receipt.create` |
| Stock items | `stock.item.manage` + `stock.item.approve` |
| Stock issues | `stock.issue` + `stock.issue.approve` |
| Stock counts | `stock.count` + `stock.adjust.approve` |
| Receive goods, approve issues | `grn.create` + `stock.issue.approve` |
| Site diaries | `site.diary.manage` + `site.diary.approve` |
| Site devices | `site.device.register` + `site.device.manage` |
| Budget revisions | `budget.revise` + `budget.approve` |
| Budget transfers | `budget.transfer` + `budget.approve` |
| Authority matrix and exceptions | `authority.manage` + `authority.approve` |
| Delegations | `delegation.create` + `delegation.approve` |
| Project stage gates | `project.gate.submit` + `project.gate.decide` |
| Cost forecasts | `forecast.prepare` + `forecast.approve` |
| Change events | `change.manage` + `change.approve` |
| Claims | `claim.manage` + `claim.approve` |
| Guarantees | `bond.manage` + `bond.approve` |
| Price adjustment | `escalation.manage` + `escalation.approve` |
| Subcontracts | `subcontract.manage` + `subcontract.approve` |
| Subcontract certificates | `subcontract.apply` + `subcontract.certify` |
| Fixed assets | `asset.manage` + `asset.approve` |
| Payroll | `payroll.prepare` + `payroll.approve` |
| Payroll and payment release | `payroll.prepare` + `payment.release` |
| Consolidation | `consolidation.manage` + `consolidation.approve` |
| Tax returns | `tax.prepare` + `tax.approve` |
| Timesheets | `timesheet.record` + `timesheet.approve` |
| Equipment usage | `equipment.record` + `equipment.approve` |
| Quality plans | `quality.manage` + `quality.approve` |
| Rules of credit | `credit.manage` + `credit.approve` |
| Document publication | `doc.manage` + `doc.approve` |

## Never delegable

`authority.approve`, `authority.manage`, `budget.override`, `delegation.approve`, `forecast.approve`, `identity.revoke_sessions`, `project.gate.decide`, `scope.tenant_wide`, `tenant.settings.manage`
