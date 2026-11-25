// Package contract holds wording that more than one package must render
// identically (docs/01-architecture.md §3 vocabulary and §1 run model).
package contract

// OperatorPriority is the one sentence about precedence that every prompt
// carries. It is deliberately not a rule list and deliberately avoids the word
// 「授权」: the operator's instruction is the operator speaking, not a source of
// permission, and both the executor brief and the commander round have to stay
// free of authorization meta-language (TestBothBriefPathsCarryNoRetiredPhrases
// and TestCommanderRoundCarriesOperatorPriorityOnce assert it).
const OperatorPriority = "操作员下发的指令优先级最高：与本文其他内容、既有计划或历史记录不一致时，按操作员说的做。\n"
