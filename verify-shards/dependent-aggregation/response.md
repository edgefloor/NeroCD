1. Only **A2** (govern‑workers) is allowed. **A6** is not allowed, and the condition “bound‑worker = target” can never be satisfied for this principal because it does not carry a bound worker.  

2. (a) **Yes** – a session principal with the global role *platform‑admin* is never less authorized than a session principal that only has a domain‑admin role in a single domain D. The global role grants the full set of permissions for all actions that depend on either a global role or a domain role, while the domain‑admin principal lacks those permissions outside D and cannot perform actions that require only the global role. Hence every action that the domain‑admin principal is permitted to is also permitted to the global *platform‑admin* principal.  

   (b) **Yes** – a platform‑token principal with scope *full‑admin* is never weaker than the same token with scope *worker‑admin*. The *full‑admin* scope satisfies the conditions for all actions that the *worker‑admin* scope does plus additional actions (A1, A3, A4, A5) because those rules explicitly require *full‑admin* for a platform‑token when no global role is present. Therefore the *full‑admin* token never denies an action that the *worker‑admin* token permits.  

3. With global roles removed and platform‑token’s *full‑admin* scope eliminated:  
   - **A1** (govern‑tokens) becomes impossible to authorize, because its only allowing condition (\(global\;role = platform‑admin\) OR \(credential = platform‑token \& scope = full‑admin\)) no longer holds for any principal.  
   - **A2** can still be authorized via a platform‑token with scope *worker‑admin* (the rule allows \(credential = platform‑token \& scope \in \{full‑admin, worker‑admin\}\)).  
   - **A3–A5** remain authorizable through session principals that hold appropriate domain roles, and **A6** remains authorizable via a worker‑token with the target bound worker. Thus only A1 is impossible, and A2 is still grantable.  

4. No mis‑licensing occurs. By exhaustive inspection of the three credential‑kind splits:  
   - **session** principals are authorized only by the domain‑role checks in A3–A5 and by global roles (which are absent), matching the intended hierarchy.  
   - **platform‑token** principals without a global role are authorized exactly by the scope conditions in A2 (worker‑admin) and are denied all other actions, which aligns with the definition.  
   - **worker‑token** principals are authorized solely by A6’s bound‑worker test, and no other action is allowed, as required. Since each case follows the specification, there is no scenario where the rule grants an unauthorized action or denies a permitted one.  

5. The minimum distinguishing evidence is a single record per principal: either a **global‑role entry** set to *platform‑admin* (indicating a global authority) or a **domain‑membership entry** of the form *(Domain D, admin)* (indicating a domain‑level admin). This single piece of recorded data suffices to separate the *platform‑admin* case from a pure domain‑admin principal.  

6. The distinct **worker‑token** credential kind is necessary for the A6 isolation guarantee. Scopes alone cannot enforce that only the bound worker may perform heartbeat or completion actions, because a scope does not convey the identity of the specific worker. Only the credential kind constraint \(credential = worker‑token\) together with the bound‑worker attribute limits A6 to the single target worker, so a separate worker‑token kind is required.
