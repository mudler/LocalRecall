package e2e_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mudler/localrag/pkg/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sashabaranov/go-openai"
)

const (
	story1 = `The Great Pigeon Heist
In the heart of Skybridge City, the pigeons ruled the skies. They weren’t just any pigeons—they were the most cunning, street-smart birds in the world. For years, humans ignored their existence as mere flapping nuisances. But beneath the fluff and feathers lay a tightly-knit society led by one infamous bird: Bertie “The Beak” Pidgewell.
Bertie was no ordinary pigeon. His feathers were a dazzling mix of slate gray and shimmering blue, and a tiny scar ran just above his left eye—a mark of the Great Bread War of ‘23. He had a sharp eye for opportunity, and today, opportunity sat in the form of a French bakery window.
Every day at 7 a.m., the bakery owner, Madame Fleur, placed freshly-baked baguettes in her window display. Every pigeon in Skybridge City dreamed of the golden loaf, but no one had ever dared to steal it. Until today.
“Alright, listen up!” Bertie called to his flock, perched on the rooftop above the bakery. “This is gonna be the most daring heist in pigeon history. You all with me?”
The flock, a scrappy mix of experienced flyers and enthusiastic young fledglings, cooed in agreement. Among them were Bertie’s trusted right wing, Sally “Wingtip” Jones, and the tech expert of the group, Ned “Peck” Thompson, who was known for his uncanny ability to disable anti-bird spikes with a few well-timed pecks.
“Step one,” Bertie explained, pacing like a general. “Sally will create a distraction. Step two, Ned disables the bakery window spikes. Step three… we fly in and grab that baguette.”
As the sun rose over Skybridge City, the pigeons swooped into action. Sally landed on the bakery’s doorstep and began performing her signature act: flapping wildly, hopping in circles, and doing what pigeons did best—looking just annoying enough to catch attention.
“Shoo! Shoo!” Madame Fleur burst out of the bakery with a broom, furiously chasing after Sally. Meanwhile, Ned flew down to the window. The spikes glinted menacingly, but with a few sharp pecks and a flick of his tail, they popped off one by one.
“Clear!” Ned signaled with a triumphant coo.
Bertie didn’t waste a second. He dove toward the golden baguette, talons outstretched. The scent of freshly baked bread filled his senses, and for a brief moment, all seemed perfect. But just as his claws closed around the loaf, a seagull appeared from nowhere.
“Oi, mate! That’s OUR score!” the seagull squawked, dive-bombing Bertie with all the grace of a clumsy fighter jet.
“Oh, no, you don’t!” Bertie growled. The pigeons and the seagull locked in an aerial dance of chaos. Feathers flew. The baguette tumbled through the air like a precious treasure.
“Catch it!” Sally screeched.
With an acrobatic loop that would put any falcon to shame, Bertie swooped down and caught the baguette just before it hit the cobblestones. He shot skyward, leaving the squawking seagull in his dust. His flock cheered wildly.
Back on the rooftop, the pigeons gathered around their prize. Bertie placed the baguette in the center like a trophy.
“Today,” he declared, “we’ve proven that pigeons aren’t just scavengers. We’re strategists. We’re fighters. We’re bread-winners!”
As the flock feasted on their spoils, a new legend was born—the tale of the Great Pigeon Heist, passed down for generations among the birds of Skybridge City.
And from that day forward, no one underestimated Bertie “The Beak” Pidgewell again.`

	story2 = `In the heart of the sleepy village of Willowgrove, tucked behind a tangle of rose bushes, lived a spider named Edgar. He was no ordinary spider—oh no. Edgar was famous (at least among the other garden creatures) for weaving webs unlike any other. His webs weren’t simple spirals; they were masterpieces of art. Some looked like stars, others like flowers, and one particularly ambitious creation resembled the village’s clock tower.
One breezy autumn morning, Edgar was hard at work designing his latest masterpiece: a web shaped like a dragon’s wing. As he spun, he noticed a curious sight—Marjorie the ladybug, flitting around his web. She was carrying a tiny scroll, which she dropped right at the base of Edgar’s favorite leaf.
“For you, Edgar!” she called before disappearing into the wind.
Edgar scuttled down the leaf, unrolled the scroll with one delicate leg, and read:
"Dear Edgar,
You are formally invited to the Grand Forest Web-Weaving Competition. Bring your finest thread and your wildest imagination!"
The competition was legendary, held deep in the forest where only the bravest spiders dared venture. Rumor had it that the winner’s web would be displayed in the ancient oak for all eternity.
Determined to make the trip, Edgar packed his satchel (which was actually just a curled-up maple leaf) and set off. The journey was treacherous. He crossed wide puddles (which looked like oceans to a spider), avoided hungry birds, and even had to navigate a particularly rude family of beetles who didn’t believe in “sharing the path.” But Edgar pressed on, his dreams of glory fueling every step.
At last, he arrived at the heart of the forest. The competitors had already gathered: sleek tarantulas, dazzling orb-weavers, and even the rare golden web spider whose silk glittered in the sunlight.
One by one, the spiders took their turns weaving. Some made webs so grand they stretched across three trees. Others made webs so delicate they looked like spun glass. Edgar watched nervously as spider after spider completed their work, each web more magnificent than the last.
Finally, it was Edgar’s turn. He took a deep breath, spun his finest silk, and began. His legs danced over branches and leaves as he worked tirelessly. Slowly, his vision came to life: a web shaped like a great phoenix, wings spread wide, flames curling at its edges. The sunlight hit the silk just right, making it look as though the firebird were soaring through the trees.
When Edgar finished, there was silence. Then—applause! The spiders cheered so loudly the forest floor trembled. Even the grumpiest tarantula gave a nod of approval.
The judges, a council of wise old spiders, huddled together before declaring Edgar the winner. His web was hung in the ancient oak, just as promised, where it shimmered like a legend in the breeze.
As Edgar headed home, tired but triumphant, he couldn’t help but smile. He had woven his dream—and it would be remembered forever.`

	testCollection = "foo"
)

var _ = Describe("API", func() {

	var (
		localAI  *openai.Client
		localRAG *client.Client
	)

	BeforeEach(func() {
		if os.Getenv("E2E") != "true" {
			Skip("Skipping E2E tests")
		}

		config := openai.DefaultConfig("foo")
		config.BaseURL = localAIEndpoint

		localAI = openai.NewClientWithConfig(config)
		localRAG = client.NewClient(localRAGEndpoint)

		Eventually(func() error {

			res, err := localAI.CreateEmbeddings(context.Background(), openai.EmbeddingRequest{
				Model: "bert-embeddings",
				Input: "foo",
			})
			if len(res.Data) == 0 {
				return fmt.Errorf("no data")
			}
			return err
		}, 5*time.Minute, time.Second).Should(Succeed())

		Eventually(func() error {
			_, err := localRAG.ListCollections()

			return err
		}, 5*time.Minute, time.Second).Should(Succeed())

		localRAG.Reset(testCollection)
	})

	It("should create collections", func() {
		err := localRAG.CreateCollection(testCollection)
		Expect(err).To(BeNil())

		collections, err := localRAG.ListCollections()
		Expect(err).To(BeNil())
		Expect(collections).To(ContainElement(testCollection))
	})

	It("should search between documents", func() {
		err := localRAG.CreateCollection(testCollection)
		Expect(err).ToNot(HaveOccurred())

		tempContent(story1, localRAG)
		tempContent(story2, localRAG)
		expectContent("foo", "spiders", "Willowgrove", localRAG)
		expectContent("foo", "heist", "The Great Pigeon Heist", localRAG)
	})
})

func tempContent(content string, localRAG *client.Client) {
	// Create a temporary file
	f, err := os.MkdirTemp("", "temp-content")
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	defer os.RemoveAll(f)

	hash := sha256.New()
	hash.Write([]byte(content))
	s := hash.Sum(nil)

	ff, err := os.Create(filepath.Join(f, fmt.Sprintf("%x.txt", s)))
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	_, err = ff.WriteString(content)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	err = localRAG.Store(testCollection, ff.Name())
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
}

func expectContent(collection, searchTerm, expected string, localRAG *client.Client) {
	docs, err := localRAG.Search(collection, searchTerm, 1)
	ExpectWithOffset(1, err).To(BeNil())
	ExpectWithOffset(1, len(docs)).To(BeNumerically("==", 1))
	ExpectWithOffset(1, docs[0].Content).To(ContainSubstring(expected))
}
