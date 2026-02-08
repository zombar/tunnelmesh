package documents

import (
	"fmt"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

// GenerateBattleReport generates realistic military battle reports with tactical analysis.
// These documents teach about military doctrine, combined arms tactics, and crisis decision-making.
func GenerateBattleReport(ctx story.Context) ([]byte, error) {
	phase := GetPhase(ctx.StoryElapsed)
	docNum := ctx.Data["DocumentNumber"].(int)
	docID := GenerateDocID("BATTLEREPORT", docNum, ctx.Timestamp)
	casualties := GetCasualtyCount(ctx.StoryElapsed)
	threatLevel := GetThreatLevel(ctx.StoryElapsed)

	var report strings.Builder

	// Classification banner
	report.WriteString(FormatClassification(ctx.Author.Clearance))
	report.WriteString("\n\n")

	// Header
	report.WriteString(fmt.Sprintf("BATTLE REPORT: %s\n", docID))
	report.WriteString(fmt.Sprintf("TIMESTAMP: %s\n", FormatTimestamp(ctx.Timestamp)))
	report.WriteString(fmt.Sprintf("PHASE: %s (H+%d)\n", PhaseName(phase), int(ctx.StoryElapsed.Hours())))
	report.WriteString(fmt.Sprintf("REPORTING OFFICER: %s\n", ctx.Author.Name))
	report.WriteString(fmt.Sprintf("THREAT LEVEL: %s\n", threatLevel))
	report.WriteString("\n")
	report.WriteString(strings.Repeat("=", 60))
	report.WriteString("\n\n")

	// Generate phase-appropriate content
	switch phase {
	case 1: // First Contact
		report.WriteString(generatePhase1BattleReport(ctx, docNum, casualties))
	case 2: // Invasion
		report.WriteString(generatePhase2BattleReport(ctx, docNum, casualties))
	case 3: // Resistance
		report.WriteString(generatePhase3BattleReport(ctx, docNum, casualties))
	}

	// Footer
	report.WriteString("\n\n")
	report.WriteString(strings.Repeat("=", 60))
	report.WriteString("\n")
	report.WriteString(FormatClassification(ctx.Author.Clearance))

	return []byte(report.String()), nil
}

func generatePhase1BattleReport(ctx story.Context, docNum, casualties int) string {
	templates := []string{
		`ENGAGEMENT: INITIAL CONTACT - DENVER SECTOR
LOCATION: %s
STATUS: ACTIVE DEFENSE

SITUATION:
At 0342 hours, our radar stations detected multiple unidentified craft entering
atmosphere at hypersonic velocities. Traditional aircraft intercept protocols
proved inadequate - these craft demonstrated maneuverability that defies our
understanding of physics. No visible propulsion systems, yet capable of
instantaneous acceleration exceeding 200G.

TACTICAL ANALYSIS:
Enemy craft employ what appears to be directed energy weapons. Visual spectrum
shows coherent blue-white beams, likely particle accelerators or focused plasma.
Impact analysis suggests temperatures exceeding 15,000°C at focal point.
Conventional armor is ineffective.

Our F-35s attempted standard BVR (Beyond Visual Range) engagement with AIM-120D
missiles. Results: 0%% effectiveness. Enemy point defense systems demonstrate
reaction times in microseconds - they're targeting our missiles before our pilots
even confirm lock. This suggests either automated AI defense or reaction times
impossible for biological entities.

LESSONS LEARNED:
- Kinetic weapons are OBSOLETE against this threat
- Electronic warfare has NO EFFECT - they're not using EM for targeting
- Dispersal tactics recommended - mass formations are death traps
- Ground forces must adopt guerrilla doctrine immediately

CASUALTIES: %d KIA, %d WIA as of this report
EQUIPMENT LOSSES: 23 aircraft, 47 ground vehicles
ENEMY LOSSES: Unconfirmed. One craft shows reduced maneuverability after
sustained 30mm GAU-8 fire from A-10. Plasma weapons may have vulnerable cooling
cycles - recommend concentrated fire during discharge.

RECOMMENDATIONS:
1. Abandon air superiority doctrine - transition to air denial
2. Disperse all units - never present concentrated targets
3. Prioritize civilian evacuation while we maintain defensive posture
4. Request immediate deployment of experimental weapons from Area 51

This is not a conventional war. We're learning to fight a completely alien
tactical doctrine with 21st century weapons. God help us.

`,
		`ENGAGEMENT: DEFENSE OF PETERSON AFB
LOCATION: %s
STATUS: FACILITY COMPROMISED

SITUATION:
Enemy conducted precision strike on Peterson AFB at 0527 hours. Unlike previous
random attacks, this demonstrated clear intelligence gathering and strategic
targeting. They knew exactly where our command and control infrastructure was
located. This raises disturbing questions about their reconnaissance capabilities.

TACTICAL ASSESSMENT:
The attack pattern reveals sophisticated combined arms doctrine:
1. First wave: High-altitude craft disable communications (EM pulse weapons)
2. Second wave: Fast-movers engage air defenses with energy weapons
3. Third wave: Slower, heavier craft deploy ground forces

Enemy ground forces encountered for FIRST TIME. Bipedal, approximately 2.4m tall,
armored exoskeletons integrating what appears to be biological and mechanical
components. Small arms fire (5.56mm NATO) shows minimal effect. 7.62mm AP rounds
demonstrate better penetration. Recommend immediate transition to .50 cal and
larger for infantry engagements.

Enemy infantry tactics: Textbook fire and maneuver. They understand cover,
concealment, suppressing fire, and flanking. This is NOT mindless alien invasion
- they're trained soldiers executing doctrinal operations. Whoever they are,
they've studied warfare extensively.

NOTABLE OBSERVATIONS:
Enemy demonstrates vulnerability to sustained automatic weapons fire. Their armor
can take 3-4 rounds of 5.56mm before failing. This means squad-level tactics
focusing fire can be effective. Additionally, they appear susceptible to
explosives - fragmentation from M67 grenades proves lethal.

One captured specimen (alive, critical condition) transferred to secure medical
facility. Recommend immediate autopsy of KIA specimens to identify vulnerabilities.

CASUALTIES: %d KIA, %d WIA (numbers increasing hourly)
Our medics are overwhelmed. Enemy energy weapons cause catastrophic tissue damage.
Burns extend deep into body cavity - victims rarely survive transport to field
hospitals.

STRATEGIC IMPLICATIONS:
If they have intelligence gathering this sophisticated, they know our entire
order of battle. Every base, every unit, every weapons cache. We must assume
total intelligence compromise and operate accordingly. Recommend immediate
dispersal of strategic assets and transition to distributed C2 structure.

`,
		`ENGAGEMENT: COUNTER-ATTACK OPERATION IRON FIST
LOCATION: %s
STATUS: TACTICAL SUCCESS, STRATEGIC STALEMATE

SITUATION:
Following 18 hours of continuous defensive operations, launched first organized
counter-attack at dawn. Operation IRON FIST targeted enemy landing zone in
Aurora suburb using combined battalion task force: armor, mechanized infantry,
and artillery support.

EXECUTION:
Phase 1 - Artillery Prep: 155mm howitzers conducted 20-minute barrage on known
enemy positions. BDA (Battle Damage Assessment) difficult due to enemy
interference with UAV feeds. Thermal imaging suggests multiple hits on enemy
positions - their craft show heat signatures vulnerable to HE rounds.

Phase 2 - Armor Assault: M1A2 Abrams tanks advanced in wedge formation, engaging
enemy craft with HEAT-MP-T rounds. SUCCESS: Main gun fire from 120mm smoothbore
CAN penetrate enemy craft hulls at ranges under 2000m. Three confirmed enemy
craft destroyed, four damaged. However, their energy weapons easily penetrate our
composite armor at any range. Loss rate: 60%%.

Phase 3 - Infantry Assault: Mechanized infantry dismounted and cleared enemy
positions using fire teams and squad tactics. Close quarters combat proves
surprisingly effective - enemy soldiers fight well at range but seem unprepared
for building-to-building fighting. Our troops adaptive, aggressive. Multiple
enemy KIA with conventional weapons.

KEY LESSONS:
The enemy is NOT invincible. They bleed. They die. Their tactics are professional
but not supernatural. We're learning to fight them, adapting doctrine in real-time.

But here's the cold truth: We're winning tactical engagements and losing the
strategic war. For every craft we destroy, two more arrive. For every landing
zone we retake, they establish three more. This is a war of attrition, and
they have numbers we can't match.

CASUALTIES: %d KIA, %d WIA - but morale is IMPROVING
Troops now understand this enemy can be killed. That knowledge is worth divisions.

RECOMMENDATION:
Continue aggressive defense. Make them pay for every meter. Buy time for
civilians to evacuate. And pray that our scientists find a way to level this
playing field, because conventional military power alone won't win this war.

`,
	}

	template := templates[docNum%len(templates)]
	coords := FormatCoordinates(1)

	kia := casualties / 3
	wia := casualties / 2

	return fmt.Sprintf(template, coords, kia, wia)
}

func generatePhase2BattleReport(ctx story.Context, docNum, casualties int) string {
	// Phase 2: Desperate battles during full invasion
	templates := []string{
		`ENGAGEMENT: BATTLE OF DENVER - DAY 2
LOCATION: %s
STATUS: CITY PARTIALLY OVERRUN, HOLDING KEY POSITIONS

SITUATION OVERVIEW:
Denver is burning. Enemy has established air superiority over metropolitan area.
Our remaining air assets conducting hit-and-run attacks from dispersed locations,
but we've lost the sky. This is now an urban ground war.

Enemy strategy has evolved. Initial attacks targeted military installations -
now they're systematically conquering civilian population centers. Pattern
suggests they're after infrastructure, not genocide. They want the planet intact
and functional. That gives us leverage if we can hold critical nodes.

CURRENT DEFENSIVE LINE:
Established improvised defensive perimeter using Interstate 25 as natural barrier.
Enemy forces pushing from north and east. We're channeling their advance into
urban kill zones where our infantry can engage on favorable terms. Tank destroyers
positioned in high-rise buildings conducting ambush operations with excellent
results.

TACTICAL INNOVATIONS:
a) "THUNDERBOLT" DOCTRINE: Hit-and-run attacks using light, mobile forces. Strike
   where they're weak, retreat before they can respond with overwhelming firepower.
   Harass, attrit, survive.

b) HARDENED STRONGPOINTS: Converted parking garages, shopping centers into
   fortified positions. Thick concrete defeats their energy weapons surprisingly
   well. Underground parking levels provide protected C2 and medical facilities.

c) NETWORKED DEFENSE: With traditional comms compromised, using mesh networks of
   encrypted tactical radios. Enemy can jam individual frequencies but can't
   suppress entire spectrum simultaneously. Distributed C2 proving resilient.

WEAPONS EFFECTIVENESS UPDATE:
- AT-4 anti-tank rockets: EFFECTIVE against enemy light craft (50%% hit rate)
- M2 .50 cal HMG: HIGHLY EFFECTIVE against enemy infantry (sustained fire)
- SMAW bunker-busters: EFFECTIVE against enemy fortified positions
- Javelin missiles: TOO SLOW - enemy point defense defeats them
- Stinger MANPADS: INEFFECTIVE - enemy craft too maneuverable

Infantry is learning to fight smart. Fire teams operating independently,
exploiting terrain, using buildings as cover. This isn't Fulda Gap - this is
Stalingrad. And our soldiers are proving that humans are the most adaptable
warriors in this solar system.

CASUALTIES: %d KIA, %d WIA, %d MIA
Medical situation critical. Field hospitals overwhelmed. Civilian medical staff
has been integrated into military operations - doctors, nurses, even veterinarians
treating combat casualties. They're heroes, every one.

CIVILIAN SITUATION:
Estimated 500,000 civilians still trapped in city. Evacuation corridors under
constant enemy fire. This is why we fight - every hour we hold gives more people
a chance to escape. We're buying time with blood.

MORALE:
Surprisingly high. Troops understand what's at stake. This isn't about politics
or oil or ideology. This is extinction-level threat. Every soldier knows that
losing means end of human civilization. That focus, that clarity of purpose -
it's transformed ordinary people into warriors.

ASSESSMENT:
We cannot hold Denver indefinitely. Enemy has unlimited reinforcements. But we
can make them pay. We can delay them. We can bleed them. And that might give
our scientists time to find a weakness, a vulnerability, something to turn this
tide. Until then, we hold the line.

No retreat. No surrender. Not one step back.

`,
	}

	template := templates[docNum%len(templates)]
	coords := FormatCoordinates(2)

	kia := casualties / 4
	wia := casualties / 3
	mia := casualties / 5

	return fmt.Sprintf(template, coords, kia, wia, mia)
}

func generatePhase3BattleReport(ctx story.Context, docNum, casualties int) string {
	// Phase 3: Organized resistance, turning the tide
	templates := []string{
		`ENGAGEMENT: OPERATION PROMETHEUS - COUNTER-OFFENSIVE
LOCATION: %s
STATUS: TACTICAL SUCCESS, ADVANCING

SITUATION:
Intelligence breakthrough from Science Division has given us a fighting chance.
Enemy craft rely on power cores that emit distinctive electromagnetic signature.
Our newly deployed EM pulse weapons can temporarily disable their systems. Window
of vulnerability: approximately 45 seconds before backup systems activate.

That 45-second window is enough.

EXECUTION:
Operation PROMETHEUS leverages this weakness in coordinated combined arms
assault:

Phase 1: EMP Strike. Adapted naval EMP weapons mounted on mobile launchers.
Fire mission at 0600 hours successfully disabled 12 enemy craft simultaneously.
They dropped like stones. Beautiful sight after two days of hell.

Phase 2: Rapid Response Force exploitation. Pre-positioned mechanized infantry
assault teams moved immediately on disabled craft. Breaching charges, close
assault, clear and secure. Captured THREE intact enemy craft and approximately
30 enemy soldiers (alive).

Phase 3: Consolidation. Engineers rigging captured craft with explosives as
backup plan, but Science Division wants them intact for study. Understanding
their technology might be key to survival.

TACTICAL LESSONS LEARNED:
The 45-second window is CRITICAL. Miss it, and they reactivate with full combat
capability. Coordination between EMP strike and ground assault must be flawless.
We've developed playbook:
- T-0:00: EMP pulse
- T+0:05: Assault force crosses line of departure
- T+0:15: Breaching phase
- T+0:30: Secured primary systems
- T+0:45: Enemy reactivation - withdraw if not secured

Our troops are executing this ballet under combat conditions. This is what
training and discipline look like.

ENEMY ADAPTION:
They're learning too. Subsequent attacks show they've deployed counter-EMP
shielding. Our window is narrowing. This is now a technological race - they
adapt, we adapt, they adapt. Darwinian warfare at light speed.

But we're still in the fight. That matters.

CASUALTIES: %d KIA, %d WIA
Our losses are dropping. Not because enemy is weaker, but because we're getting
better at this. Experience, doctrine, adaptation - military fundamentals still
apply even in interplanetary war.

INTELLIGENCE WINDFALL:
Captured enemy soldiers are... talking. Reluctantly, but talking. Turns out
they're not some hive mind - they're individuals with fears, doubts, chain of
command. One prisoner (designation: Subject-7) indicated this invasion was
controversial even among their leadership. Faction opposed to war exists.

This changes everything. If they have politics, they have pressure points.
Maybe this war can end with something other than mutual extinction.

STRATEGIC ASSESSMENT:
We're not winning. But we're not losing anymore either. We've stabilized the
front, developed effective counter-tactics, captured enemy technology. The
bleeding has stopped. Now comes the hard part: pushing them back.

Can we do it? Unknown. But for the first time since this nightmare began, I
believe it's possible. Our troops believe it. And belief, General, is a weapon
too.

Recommend: Continue aggressive operations. Press the advantage before they adapt
further. And get our scientists whatever they need - they're the ones who'll
actually win this war.

`,
	}

	template := templates[docNum%len(templates)]
	coords := FormatCoordinates(3)

	kia := casualties / 10 // Fewer casualties in Phase 3
	wia := casualties / 8

	return fmt.Sprintf(template, coords, kia, wia)
}
