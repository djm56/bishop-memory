// Package api — crew metrics computed on read (GET /v1/crew).
//
// Returns per-agent metrics including steps run, review rates, mistakes, and service records.
// Aggregates data from mission_steps, agent_mistakes, mission_grades, and service_records.
package api

import (
	"database/sql"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"

	"bishop-memory/internal/importer"
	"bishop-memory/internal/review"
)

// crewHandler handles GET /v1/crew.
//
// Returns per-agent metrics: steps run (status='done'), steps total, reviewed steps,
// first-pass approvals, re-reviews, mistakes by impact level (high/med/low/unrated),
// service records (self-reported vs bishop-observed), average grade on graded missions,
// and last activity timestamp.
//
// Definitions:
//   - Reviews: per mission, an apone review with non-empty ReviewsStep is matched to the
//     mission_steps row whose step equals ReviewsStep; credit the agent of that step.
//     Reviews with no ReviewsStep, no matching step, or Verdict not approved/not-approved
//     are excluded from reviewed_steps count.
//   - First pass: order reviews for each reviewed step by mission_steps.id (insertion order).
//     The first review's verdict (approved or not) sets first_pass_approved. Subsequent
//     reviews for the same step are re-reviews.
//   - re_reviews per reviewed step: if any of the step's reviews has FixRound != nil, use the
//     max FixRound across those reviews (a single review carrying fix_round=N gives N);
//     otherwise use count-1 (0 for a single review). Sum per agent.
//   - Mistakes: impact values outside {high,med,low} count as unrated; agent is normalised
//     via CrewName. Top 5 mistakes ordered high>med>low>unrated, then mission_id desc, then
//     ord asc. (mission_id, ord) is the table's primary key, so the order is total.
//   - Service records: normalised agent via CrewName; split source by "self-reported" vs
//     "bishop-observed" (match exact strings in service_records table).
//   - Average grade: distinct missions where agent has >=1 step, joined to mission_grades
//     with state='graded'; average of gradePoints; null if no graded missions.
//   - Last active: max(mission_steps.ended_at) across the agent's steps, or null if none of
//     them has a non-empty ended_at.
//   - Agents: union of names in mission_steps, agent_mistakes, service_records and the crew
//     table. Every source is normalised via CrewName and a name that normalises to empty is
//     dropped. A crew row with a NULL role, or no crew row at all, gives role null. When
//     several crew rows normalise to one name, a non-null role beats a null one, and among
//     non-null roles the row first in name order (byte order) wins.
//     Order: steps_run desc, then agent asc.
func crewHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		// Fetch all steps, normalised by agent name.
		stepsRows, err := queryMaps(ctx, db, `
			SELECT CAST(id AS TEXT) AS step_id, mission_id, step, agent, status, notes, summary, ended_at
			  FROM mission_steps
			 ORDER BY id
		`)
		if err != nil {
			internalError(c, err)
			return
		}

		// Fetch all agent mistakes.
		mistakesRows, err := queryMaps(ctx, db, `
			SELECT mission_id, ord, agent, mistake, impact
			  FROM agent_mistakes
		`)
		if err != nil {
			internalError(c, err)
			return
		}

		// Fetch all service records.
		recordsRows, err := queryMaps(ctx, db, `
			SELECT agent, source
			  FROM service_records
		`)
		if err != nil {
			internalError(c, err)
			return
		}

		// Fetch crew table for role mapping.
		crewRows, err := queryMaps(ctx, db, `
			SELECT name, role FROM crew ORDER BY name
		`)
		if err != nil {
			internalError(c, err)
			return
		}
		// crewRoles holds every normalised, non-empty crew-table name; the value is nil
		// when no row for the name has a role. Rows arrive in name order and a name already
		// holding a role keeps it, so a NULL-role row never erases a role and the first
		// non-null role in name order wins.
		crewRoles := map[string]*string{}
		for _, row := range crewRows {
			name, ok := row["name"].(string)
			if !ok {
				continue
			}
			name = importer.CrewName(name)
			if name == "" {
				continue
			}
			if roleVal, ok := row["role"].(string); ok {
				if crewRoles[name] == nil {
					crewRoles[name] = &roleVal
				}
			} else if _, seen := crewRoles[name]; !seen {
				crewRoles[name] = nil
			}
		}

		// Fetch mission grades.
		gradesRows, err := queryMaps(ctx, db, `
			SELECT mission_id, state, grade
			  FROM mission_grades
			 WHERE state = 'graded'
		`)
		if err != nil {
			internalError(c, err)
			return
		}

		// Build per-agent metrics.
		type agentData struct {
			role              *string
			stepsRun          int
			stepsTotal        int
			reviewedSteps     int
			firstPassApproved int
			reReviews         int
			mistakesByImpact  map[string]int
			topMistakes       []map[string]any
			selfReported      int
			bishopObserved    int
			gradeTotal        float64
			gradedMissions    int
			lastActive        sql.NullString
		}

		agents := map[string]*agentData{}

		// Collect seen agent names from all sources.
		seenAgents := map[string]bool{}
		for _, row := range stepsRows {
			if agent, ok := row["agent"].(string); ok {
				if name := importer.CrewName(agent); name != "" {
					seenAgents[name] = true
				}
			}
		}
		for _, row := range mistakesRows {
			if agent, ok := row["agent"].(string); ok {
				if name := importer.CrewName(agent); name != "" {
					seenAgents[name] = true
				}
			}
		}
		for _, row := range recordsRows {
			if agent, ok := row["agent"].(string); ok {
				if name := importer.CrewName(agent); name != "" {
					seenAgents[name] = true
				}
			}
		}
		for name := range crewRoles {
			seenAgents[name] = true
		}

		// Initialize agent data structures.
		for name := range seenAgents {
			agents[name] = &agentData{
				role:             crewRoles[name],
				mistakesByImpact: map[string]int{},
				topMistakes:      []map[string]any{},
			}
		}

		// Process steps: count total, count done, and process reviews.
		type reviewKey struct {
			missionID string
			stepID    string // The step being reviewed
		}
		reviewsByKey := map[reviewKey][]map[string]any{}

		for _, row := range stepsRows {
			agentStr, _ := row["agent"].(string)
			agentName := importer.CrewName(agentStr)
			if agentName == "" {
				continue
			}
			agent := agents[agentName]

			statusVal, _ := row["status"].(string)
			agent.stepsTotal++
			if statusVal == "done" {
				agent.stepsRun++
			}

			// If this is an apone review step, parse it and collect for matching.
			if importer.CrewName(agentStr) == "apone" {
				notes, _ := row["notes"].(string)
				summary, _ := row["summary"].(string)
				v := review.Parse(notes, summary)

				// Only count as a review if the verdict is approved or not-approved and a
				// step is named. A review naming no step must stay excluded: a legacy
				// mission_steps row with a NULL step is keyed "" in stepsMap below and
				// would otherwise be credited.
				if (v.Verdict == review.Approved || v.Verdict == review.NotApproved) &&
					v.ReviewsStep != "" {

					missionID, _ := row["mission_id"].(string)
					key := reviewKey{missionID: missionID, stepID: v.ReviewsStep}
					reviewsByKey[key] = append(reviewsByKey[key], map[string]any{
						"step_id":   row["step_id"],
						"verdict":   v.Verdict,
						"fix_round": v.FixRound,
					})
				}
			}

			// Track last activity.
			if endedAt, ok := row["ended_at"].(string); ok && endedAt != "" {
				if agent.lastActive.String == "" || endedAt > agent.lastActive.String {
					agent.lastActive = sql.NullString{String: endedAt, Valid: true}
				}
			}
		}

		// Process reviews: determine first-pass approval and re-reviews per reviewed step.
		// For each reviewed step, the first review in insertion order (step_id) determines
		// first_pass_approved; subsequent reviews are re-reviews.
		// Build a map from reviewed step to the agent of the step being reviewed.
		stepsMap := map[string]map[string]any{} // mission_id + ":" + step -> row
		for _, row := range stepsRows {
			missionID, _ := row["mission_id"].(string)
			step, _ := row["step"].(string)
			key := missionID + ":" + step
			stepsMap[key] = row
		}

		// Now process reviews and credit them.
		for key, reviews := range reviewsByKey {
			missionID := key.missionID
			reviewedStep := key.stepID

			// Find the step being reviewed and credit its agent.
			stepKey := missionID + ":" + reviewedStep
			if stepRow, ok := stepsMap[stepKey]; ok {
				if stepAgent, ok := stepRow["agent"].(string); ok {
					creditedName := importer.CrewName(stepAgent)
					if creditedName != "" {
						// Count first pass and re-reviews.
						credited := agents[creditedName]
						credited.reviewedSteps++
						if v, ok := reviews[0]["verdict"].(string); ok && v == review.Approved {
							credited.firstPassApproved++
						}

						// Re-reviews: max FixRound across the step's reviews if any carries
						// one, else count-1.
						maxFixRound := 0
						hasFixRound := false
						for _, rev := range reviews {
							if fr, ok := rev["fix_round"].(*int); ok && fr != nil {
								hasFixRound = true
								if *fr > maxFixRound {
									maxFixRound = *fr
								}
							}
						}
						if hasFixRound {
							credited.reReviews += maxFixRound
						} else {
							credited.reReviews += len(reviews) - 1
						}
					}
				}
			}
		}

		// Process mistakes.
		// ord is the sort tiebreak and is not part of the response item.
		type mistakeEntry struct {
			ord  int64
			item map[string]any
		}
		mistakeLists := map[string][]mistakeEntry{} // agent -> list of mistakes
		for _, row := range mistakesRows {
			agentStr, _ := row["agent"].(string)
			agentName := importer.CrewName(agentStr)
			if agentName == "" {
				continue
			}
			agent := agents[agentName]

			impact, _ := row["impact"].(string)
			// A NULL impact reads as "". The schema CHECK permits only NULL, low, med and
			// high, so every row that lands in unrated here has a NULL impact.
			if impact != "high" && impact != "med" && impact != "low" {
				impact = "unrated"
			}
			agent.mistakesByImpact[impact]++

			missionID, _ := row["mission_id"].(string)
			mistakeTxt, _ := row["mistake"].(string)
			ord, _ := row["ord"].(int64)
			mistakeLists[agentName] = append(mistakeLists[agentName], mistakeEntry{
				ord: ord,
				item: map[string]any{
					"mission_id": missionID,
					"mistake":    mistakeTxt,
					"impact":     impact,
				},
			})
		}

		// Build top mistakes per agent (capped at 5), ordered by impact, then mission_id desc,
		// then ord asc.
		for agentName, agent := range agents {
			if mistakes, ok := mistakeLists[agentName]; ok {
				// Sort: high > med > low > unrated, then mission_id desc, then ord asc.
				// (mission_id, ord) is unique, so no two entries compare equal.
				impactOrder := map[string]int{"high": 0, "med": 1, "low": 2, "unrated": 3}
				sort.Slice(mistakes, func(i, j int) bool {
					impI := impactOrder[mistakes[i].item["impact"].(string)]
					impJ := impactOrder[mistakes[j].item["impact"].(string)]
					if impI != impJ {
						return impI < impJ
					}
					mI := mistakes[i].item["mission_id"].(string)
					mJ := mistakes[j].item["mission_id"].(string)
					if mI != mJ {
						return mI > mJ
					}
					return mistakes[i].ord < mistakes[j].ord
				})
				// Cap at 5.
				if len(mistakes) > 5 {
					mistakes = mistakes[:5]
				}
				top := make([]map[string]any, 0, len(mistakes))
				for _, m := range mistakes {
					top = append(top, m.item)
				}
				agent.topMistakes = top
			}
		}

		// Process service records.
		for _, row := range recordsRows {
			agentStr, _ := row["agent"].(string)
			agentName := importer.CrewName(agentStr)
			if agentName == "" {
				continue
			}
			agent := agents[agentName]

			source, _ := row["source"].(string)
			if source == "self-reported" {
				agent.selfReported++
			} else if source == "bishop-observed" {
				agent.bishopObserved++
			}
		}

		// Process mission grades: average grade per agent (only graded missions).
		// Build mission -> set of agents index from stepsRows (O(n) instead of O(n*m)).
		missionAgents := map[string]map[string]bool{} // mission_id -> agent name -> true
		for _, stepRow := range stepsRows {
			missionID, _ := stepRow["mission_id"].(string)
			agentStr, _ := stepRow["agent"].(string)
			agentName := importer.CrewName(agentStr)
			if agentName == "" {
				continue
			}
			if missionAgents[missionID] == nil {
				missionAgents[missionID] = map[string]bool{}
			}
			missionAgents[missionID][agentName] = true
		}

		// For each agent, track graded missions (once per mission per agent).
		agentGradedMissions := map[string]map[string]bool{} // agent -> mission_id -> true
		for agentName := range agents {
			agentGradedMissions[agentName] = map[string]bool{}
		}

		// For each graded mission, find all agents who worked on it.
		for _, gradRow := range gradesRows {
			gradeStr, _ := gradRow["grade"].(string)
			missionID, _ := gradRow["mission_id"].(string)

			// Use index to find agents for this mission.
			if agentsInMission, ok := missionAgents[missionID]; ok {
				for agentName := range agentsInMission {
					agent := agents[agentName]

					// Mark this mission as graded for this agent (once per mission).
					if !agentGradedMissions[agentName][missionID] {
						agentGradedMissions[agentName][missionID] = true
						// Accumulate grade points (once per mission per agent).
						if points, ok := gradePoints[gradeStr]; ok {
							agent.gradeTotal += points
							agent.gradedMissions++
						}
					}
				}
			}
		}

		// Build response.
		type mistakesData struct {
			High    int              `json:"high"`
			Med     int              `json:"med"`
			Low     int              `json:"low"`
			Unrated int              `json:"unrated"`
			Top     []map[string]any `json:"top"`
		}

		type crewRow struct {
			Agent             string         `json:"agent"`
			Role              *string        `json:"role"`
			StepsRun          int            `json:"steps_run"`
			StepsTotal        int            `json:"steps_total"`
			ReviewedSteps     int            `json:"reviewed_steps"`
			FirstPassApproved int            `json:"first_pass_approved"`
			FirstPassRate     *float64       `json:"first_pass_rate"`
			ReReviews         int            `json:"re_reviews"`
			Mistakes          mistakesData   `json:"mistakes"`
			ServiceRecords    map[string]int `json:"service_records"`
			GradedMissions    int            `json:"graded_missions"`
			AvgPoints         *float64       `json:"avg_points"`
			AvgGrade          *string        `json:"avg_grade"`
			LastActive        *string        `json:"last_active"`
		}

		var result []crewRow
		for name, agent := range agents {
			row := crewRow{
				Agent:             name,
				Role:              agent.role,
				StepsRun:          agent.stepsRun,
				StepsTotal:        agent.stepsTotal,
				ReviewedSteps:     agent.reviewedSteps,
				FirstPassApproved: agent.firstPassApproved,
				ReReviews:         agent.reReviews,
				Mistakes: mistakesData{
					High:    agent.mistakesByImpact["high"],
					Med:     agent.mistakesByImpact["med"],
					Low:     agent.mistakesByImpact["low"],
					Unrated: agent.mistakesByImpact["unrated"],
					Top:     agent.topMistakes,
				},
				ServiceRecords: map[string]int{
					"total":           agent.selfReported + agent.bishopObserved,
					"self_reported":   agent.selfReported,
					"bishop_observed": agent.bishopObserved,
				},
				GradedMissions: agent.gradedMissions,
			}

			// Set first_pass_rate (null if reviewed_steps==0).
			if agent.reviewedSteps > 0 {
				rate := float64(agent.firstPassApproved) / float64(agent.reviewedSteps)
				row.FirstPassRate = &rate
			}

			// Set avg_points and avg_grade (null if graded_missions==0).
			if agent.gradedMissions > 0 {
				avg := agent.gradeTotal / float64(agent.gradedMissions)
				row.AvgPoints = &avg
				grade := pointsToGrade(avg)
				row.AvgGrade = &grade
			}

			// Set last_active, nullable.
			if agent.lastActive.Valid {
				row.LastActive = &agent.lastActive.String
			}

			result = append(result, row)
		}

		// Sort: steps_run desc, then agent asc.
		sort.Slice(result, func(i, j int) bool {
			if result[i].StepsRun != result[j].StepsRun {
				return result[i].StepsRun > result[j].StepsRun
			}
			return result[i].Agent < result[j].Agent
		})

		// Ensure non-nil slice even when empty (so JSON is `[]` not `null`).
		if result == nil {
			result = []crewRow{}
		}

		c.JSON(http.StatusOK, gin.H{"crew": result})
	}
}
