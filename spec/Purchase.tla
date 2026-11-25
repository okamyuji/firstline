---------------------------- MODULE Purchase ----------------------------
(* Purchase state machine of docs/design/basic-design.md, section 状態遷移.
   Rows that change no state (payment_id recording, duplicate completion
   notices, failure notices outside held, failure notices for unknown
   purchases, an existing orphan row) are stuttering steps and are omitted.
   Time is abstract: pastDue[b] turns TRUE at some point while b is held,
   and paidLate[b] latches it when the payment completes, because
   Completion.At does not change with redelivery or reconciliation. *)
EXTENDS Naturals, FiniteSets

CONSTANTS Capacity, Buyers

MaxQuantity == 4

VARIABLES
    st,          \* purchase state per buyer; "none" means no record yet
    qty,         \* seats of the purchase
    paid,        \* the payment service completed the payment
    paidLate,    \* Completion.At was at or after expires_at
    notified,    \* the completion was processed (notice or reconcile)
    refundDone,  \* Payment.Refund succeeded for the purchase
    pastDue,     \* the clock has passed expires_at
    giveBacks,   \* how many times the purchase gave its seats back
    remaining,   \* event_sales.remaining
    orphan       \* a completed payment with no purchase record

vars == <<st, qty, paid, paidLate, notified, refundDone, pastDue,
          giveBacks, remaining, orphan>>

Init ==
    /\ st = [b \in Buyers |-> "none"]
    /\ qty = [b \in Buyers |-> 0]
    /\ paid = [b \in Buyers |-> FALSE]
    /\ paidLate = [b \in Buyers |-> FALSE]
    /\ notified = [b \in Buyers |-> FALSE]
    /\ refundDone = [b \in Buyers |-> FALSE]
    /\ pastDue = [b \in Buyers |-> FALSE]
    /\ giveBacks = [b \in Buyers |-> 0]
    /\ remaining = Capacity
    /\ orphan = "none"

\* Leaving held with the seats returned; the only way to return seats.
GiveBack(b, to) ==
    /\ st' = [st EXCEPT ![b] = to]
    /\ giveBacks' = [giveBacks EXCEPT ![b] = @ + 1]
    /\ remaining' = remaining + qty[b]

Hold(b) ==
    /\ st[b] = "none"
    /\ \E q \in 1..MaxQuantity :
        /\ remaining >= q
        /\ qty' = [qty EXCEPT ![b] = q]
        /\ remaining' = remaining - q
    /\ st' = [st EXCEPT ![b] = "held"]
    /\ UNCHANGED <<paid, paidLate, notified, refundDone, pastDue,
                   giveBacks, orphan>>

StartFails(b) ==
    /\ st[b] = "held"
    /\ GiveBack(b, "released")
    /\ UNCHANGED <<qty, paid, paidLate, notified, refundDone, pastDue, orphan>>

FailureNotice(b) ==
    /\ st[b] = "held"
    /\ GiveBack(b, "released")
    /\ UNCHANGED <<qty, paid, paidLate, notified, refundDone, pastDue, orphan>>

\* Environment: the clock passes expires_at.
Deadline(b) ==
    /\ st[b] = "held"
    /\ ~pastDue[b]
    /\ pastDue' = [pastDue EXCEPT ![b] = TRUE]
    /\ UNCHANGED <<st, qty, paid, paidLate, notified, refundDone,
                   giveBacks, remaining, orphan>>

\* Environment: the payment service completes a payment that was started.
PaymentCompletes(b) ==
    /\ st[b] \in {"held", "expired", "released"}
    /\ ~paid[b]
    /\ paid' = [paid EXCEPT ![b] = TRUE]
    /\ paidLate' = [paidLate EXCEPT ![b] = pastDue[b]]
    /\ UNCHANGED <<st, qty, notified, refundDone, pastDue, giveBacks,
                   remaining, orphan>>

Expire(b) ==
    /\ st[b] = "held"
    /\ pastDue[b]
    /\ GiveBack(b, "expired")
    /\ UNCHANGED <<qty, paid, paidLate, notified, refundDone, pastDue, orphan>>

CompletedInTime(b) ==
    /\ st[b] = "held"
    /\ ~paidLate[b]
    /\ st' = [st EXCEPT ![b] = "purchased"]
    /\ UNCHANGED <<giveBacks, remaining>>

CompletedLate(b) ==
    /\ st[b] = "held"
    /\ paidLate[b]
    /\ GiveBack(b, "refunding")

CompletedAfterRelease(b) ==
    /\ st[b] \in {"expired", "released"}
    /\ st' = [st EXCEPT ![b] = "refunding"]
    /\ UNCHANGED <<giveBacks, remaining>>

\* The completion notice and the reconcile run the same handling; only the
\* reconcile is fair, because a notice can be lost when the process dies.
HandleCompletion(b) ==
    /\ paid[b]
    /\ ~notified[b]
    /\ \/ CompletedInTime(b)
       \/ CompletedLate(b)
       \/ CompletedAfterRelease(b)
    /\ notified' = [notified EXCEPT ![b] = TRUE]
    /\ UNCHANGED <<qty, paid, paidLate, refundDone, pastDue, orphan>>

CompletionNotice(b) == HandleCompletion(b)

Reconcile(b) == HandleCompletion(b)

RefundSucceeds(b) ==
    /\ st[b] = "refunding"
    /\ st' = [st EXCEPT ![b] = "refunded"]
    /\ refundDone' = [refundDone EXCEPT ![b] = TRUE]
    /\ UNCHANGED <<qty, paid, paidLate, notified, pastDue, giveBacks,
                   remaining, orphan>>

\* Environment: a payment completes for a purchase ID with no record.
OrphanPaymentCompletes ==
    /\ orphan = "none"
    /\ orphan' = "paid"
    /\ UNCHANGED <<st, qty, paid, paidLate, notified, refundDone, pastDue,
                   giveBacks, remaining>>

OrphanCompletion ==
    /\ orphan = "paid"
    /\ orphan' = "refunding"
    /\ UNCHANGED <<st, qty, paid, paidLate, notified, refundDone, pastDue,
                   giveBacks, remaining>>

OrphanNotice == OrphanCompletion

OrphanReconcile == OrphanCompletion

OrphanRefundSucceeds ==
    /\ orphan = "refunding"
    /\ orphan' = "refunded"
    /\ UNCHANGED <<st, qty, paid, paidLate, notified, refundDone, pastDue,
                   giveBacks, remaining>>

Next ==
    \/ \E b \in Buyers :
        \/ Hold(b)
        \/ StartFails(b)
        \/ FailureNotice(b)
        \/ Deadline(b)
        \/ PaymentCompletes(b)
        \/ Expire(b)
        \/ CompletionNotice(b)
        \/ Reconcile(b)
        \/ RefundSucceeds(b)
    \/ OrphanPaymentCompletes
    \/ OrphanNotice
    \/ OrphanReconcile
    \/ OrphanRefundSucceeds

\* Refund retry fairness also encodes the assumption that Payment.Refund
\* eventually succeeds.
Fairness ==
    /\ \A b \in Buyers :
        /\ WF_vars(Deadline(b))
        /\ WF_vars(Expire(b))
        /\ WF_vars(Reconcile(b))
        /\ WF_vars(RefundSucceeds(b))
    /\ WF_vars(OrphanReconcile)
    /\ WF_vars(OrphanRefundSucceeds)

Spec == Init /\ [][Next]_vars /\ Fairness

Seats(S) == LET holders == {b \in Buyers : st[b] \in S}
                Sum[T \in SUBSET holders] ==
                    IF T = {} THEN 0
                    ELSE LET x == CHOOSE x \in T : TRUE
                         IN qty[x] + Sum[T \ {x}]
            IN Sum[holders]

NoOversell == Seats({"held", "purchased"}) <= Capacity

ReleaseOnce == \A b \in Buyers : giveBacks[b] <= 1

PaidEndsWell ==
    /\ \A b \in Buyers : paid[b] ~> st[b] \in {"purchased", "refunded"}
    /\ orphan = "paid" ~> orphan = "refunded"

=============================================================================
